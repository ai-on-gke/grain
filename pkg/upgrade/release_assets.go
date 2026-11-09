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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/installtrack"
)

// ImageSource selects where ModeImage fetches candidate daemon and sandbox
// container images from.
type ImageSource string

const (
	// ImageSourceRegistry pulls container images from an OCI registry with
	// `docker pull` (the default).
	ImageSourceRegistry ImageSource = "registry"
	// ImageSourceReleaseAssets downloads gzip `docker save` tarballs attached
	// as release assets on a GitHub or GitHub Enterprise repository and loads
	// them with `docker load`.
	ImageSourceReleaseAssets ImageSource = "release-assets"
)

const (
	// DefaultReleaseManifestAsset is the v1 release-asset manifest filename.
	DefaultReleaseManifestAsset = "images.json"
	// DefaultChecksumSuffix is appended to a tarball asset name to locate its
	// sha256sum sidecar asset when ChecksumAsset is unset.
	DefaultChecksumSuffix = ".sha256"
	// ReleaseManifestSchemaV1 is the schema version required on images.json.
	ReleaseManifestSchemaV1 = 1
	// ReleaseManifestSchemaV2 adds a per-image "release": the immutable
	// release (sha-<short> or vX.Y.Z) holding that image's asset, so a
	// moving channel release can carry images.json alone.
	ReleaseManifestSchemaV2 = 2
)

// Roles named in images.json (schema 1).
const (
	ReleaseRoleGrain      = "grain"
	ReleaseRoleGuest      = "guest"
	ReleaseRoleK8sSandbox = "k8s-sandbox"
)

const (
	releaseMetadataTimeout = 30 * time.Second
	releaseDownloadTimeout = 30 * time.Minute
	dockerLoadTimeout      = 15 * time.Minute
	minStagingHeadroom     = 64 << 20 // 64 MiB headroom above tarball size
)

var (
	hex64Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex40Pattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	fullImageIDRegex = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

	// errNoReleaseImageAssets marks a target release that either does not exist
	// on the repository or carries no images.json manifest asset. Under the
	// default (unpinned) image source, upgrades fall back to registry pulls on
	// this condition and report "release has no image assets; pulled from <registry>".
	errNoReleaseImageAssets = errors.New("release has no image assets")

	// errReleaseIntegrity marks a malformed manifest, missing checksum sidecar,
	// SHA-256 mismatch, or Docker image ID mismatch that must never silently
	// fall back to another source.
	errReleaseIntegrity = errors.New("release asset integrity check failed")

	// errReleaseMidUpdate marks a moving release (channel-<branch>, latest)
	// that exists but has no images.json: the publisher deletes the
	// manifest first and uploads it last when it re-points one, so this is
	// a release being replaced, not one without image assets. It never
	// falls back to a registry; the next check tries again.
	errReleaseMidUpdate = errors.New("moving release is mid-update")

	// errReleaseNotPublished marks an immutable release (vX.Y.Z,
	// sha-<short>) that was tagged without its image assets -- its
	// publisher writes every tag, and an empty release whose body says
	// "tagged, not published", while asset publishing is off -- on a
	// deployment that loads release assets with no registry to fall back
	// to. It always travels with errNoReleaseImageAssets and names the
	// newest release that was published.
	errReleaseNotPublished = errors.New("release is not published")

	// ErrImageSourceLocked is returned when SetImageSource is called on an
	// Upgrader whose image source was locked by a daemon flag or environment variable.
	ErrImageSourceLocked = errors.New("upgrade image source is set by a daemon flag or environment variable and cannot be modified")

	// errReleaseAssetsNeedOperator refuses an override from the upgrade
	// pane that would move a deployment with -upgrade-verify-cmd onto
	// release assets. Its signatures are over registry manifests and pass
	// a loaded tarball only when its image ID is a signed digest
	// (verifyLoaded), and `docker load` re-tags whatever the tarball names
	// before any verifier has run: whether a release's tarballs meet that
	// is the host operator's to say, with -upgrade-image-source, not
	// something a principal holding deployment:upgrade may decide for the
	// verifier that operator configured.
	errReleaseAssetsNeedOperator = errors.New("this deployment verifies image signatures (-upgrade-verify-cmd), so loading release-asset tarballs takes the daemon's -upgrade-image-source=release-assets or -upgrade-image-fallback=release-assets flag rather than the upgrade pane")
)

// ImageSourceOverride is the operator-editable subset of ImageConfig's image
// source settings persisted in ImageConfig.SourceOverrideFile when SourceLocked is false.
type ImageSourceOverride struct {
	// Mode is "" (default: release-assets with missing-manifest fallback to
	// registry), "release-assets" (explicit release-assets), or "registry"
	// (explicit registry pull).
	Mode              string `json:"mode,omitempty"`
	Fallback          string `json:"fallback,omitempty"`
	FallbackExplicit  bool   `json:"fallbackExplicit,omitempty"`
	ReleaseRepository string `json:"releaseRepository,omitempty"`
	ReleaseAsset      string `json:"releaseAsset,omitempty"`
	Checksum          string `json:"checksum,omitempty"`
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
	if len(u.cfg.Image.VerifyCmd) > 0 && (mode == string(ImageSourceReleaseAssets) ||
		fb == ImageSourceReleaseAssets && u.operatorFallback != ImageSourceReleaseAssets) {
		return errReleaseAssetsNeedOperator
	}
	relRepo := strings.TrimSpace(ov.ReleaseRepository)
	if relRepo != "" {
		if err := u.validateReleaseRepositoryLocked(relRepo); err != nil {
			return err
		}
	}
	clean := ImageSourceOverride{
		Mode:              mode,
		Fallback:          string(fb),
		FallbackExplicit:  ov.FallbackExplicit,
		ReleaseRepository: relRepo,
		ReleaseAsset:      strings.TrimSpace(ov.ReleaseAsset),
		Checksum:          strings.TrimSpace(ov.Checksum),
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
	// Written before this deployment was given -upgrade-verify-cmd, or by
	// hand: the same line SetImageSource draws.
	if len(u.cfg.Image.VerifyCmd) > 0 {
		if norm, err := NormalizeImageSource(ov.Mode); err == nil && ov.Mode != "" && norm == ImageSourceReleaseAssets {
			log.Printf("grain upgrade: ignoring image source %q in %s: %v", ov.Mode, p, errReleaseAssetsNeedOperator)
			ov.Mode = ""
		}
		if fb, err := ParseImageFallback(ov.Fallback); err == nil && fb == ImageSourceReleaseAssets && u.operatorFallback != ImageSourceReleaseAssets {
			log.Printf("grain upgrade: ignoring fallback %q in %s: %v", ov.Fallback, p, errReleaseAssetsNeedOperator)
			ov.Fallback, ov.FallbackExplicit = "", false
		}
	}
	if repo := strings.TrimSpace(ov.ReleaseRepository); repo != "" {
		if err := u.validateReleaseRepositoryLocked(repo); err != nil {
			log.Printf("grain upgrade: ignoring the release repository in %s: %v", p, err)
			ov.ReleaseRepository = ""
		}
	}
	u.applyImageSourceOverrideLocked(ov)
}

// validateReleaseRepositoryLocked is whether an override from the upgrade
// pane may name repo as the release repository image upgrades load from.
//
// A release-asset tarball is checked only against the images.json and
// sidecar published beside it, so the release repository decides what
// this deployment boots exactly as its image repository does -- and an
// image repository is held to the ones it may move onto
// (installtrack.ValidateTargetRepository). So is this: repo must be the
// one the daemon's flags or its track name, or the release repository
// of an image repository this deployment may upgrade onto. Anything else
// takes a daemon flag (-upgrade-release-repo), which only the operator
// of the host sets.
func (u *Upgrader) validateReleaseRepositoryLocked(repo string) error {
	host, owner, name, err := ParseReleaseRepository(repo)
	if err != nil {
		return err
	}
	want := strings.ToLower(host + "/" + owner + "/" + name)
	var allowed []string
	add := func(r string) {
		if h, o, n, err := ParseReleaseRepository(r); err == nil {
			allowed = append(allowed, strings.ToLower(h+"/"+o+"/"+n))
		}
	}
	add(u.operatorReleaseAssets.Repository)
	if d, ok := TrackDefaultsFor(u.cfg.Image.Track); ok {
		add(d.ReleaseRepository)
	}
	if eff, err := u.effectiveRepository(); err == nil {
		add(ReleaseRepositoryForImage(eff))
	}
	for _, r := range installtrack.KnownSuccessorRepositories(u.cfg.Image.AllowedRepositories...) {
		if u.validateRepository(r) == nil {
			add(ReleaseRepositoryForImage(r))
		}
	}
	if slices.Contains(allowed, want) {
		return nil
	}
	slices.Sort(allowed)
	allowed = slices.Compact(allowed)
	if len(allowed) == 0 {
		return fmt.Errorf("release repository %q is not one this deployment may upgrade from: none is configured for this image repository (set one with the daemon's -upgrade-release-repo flag)", repo)
	}
	return fmt.Errorf("release repository %q is not one this deployment may upgrade from; allowed are %s (set another with the daemon's -upgrade-release-repo flag)",
		repo, strings.Join(allowed, ", "))
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
	// A field the override leaves empty goes back to what the daemon's
	// flags set, rather than clearing -upgrade-release-repo and the rest.
	op := u.operatorReleaseAssets
	u.cfg.Image.ReleaseAssets.Repository = op.Repository
	if r := strings.TrimSpace(ov.ReleaseRepository); r != "" {
		u.cfg.Image.ReleaseAssets.Repository = r
	}
	u.cfg.Image.ReleaseAssets.Asset = op.Asset
	if a := strings.TrimSpace(ov.ReleaseAsset); a != "" {
		u.cfg.Image.ReleaseAssets.Asset = a
	}
	chk := strings.TrimSpace(ov.Checksum)
	if strings.HasPrefix(chk, ".") {
		u.cfg.Image.ReleaseAssets.ChecksumSuffix = chk
		u.cfg.Image.ReleaseAssets.ChecksumAsset = ""
	} else if chk != "" {
		u.cfg.Image.ReleaseAssets.ChecksumAsset = chk
		u.cfg.Image.ReleaseAssets.ChecksumSuffix = ""
	} else {
		u.cfg.Image.ReleaseAssets.ChecksumAsset = op.ChecksumAsset
		u.cfg.Image.ReleaseAssets.ChecksumSuffix = op.ChecksumSuffix
	}
}

// NormalizeImageSource normalizes a configured image source, defaulting empty
// to ImageSourceReleaseAssets.
func NormalizeImageSource(raw string) (ImageSource, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ImageSourceReleaseAssets):
		return ImageSourceReleaseAssets, nil
	case string(ImageSourceRegistry):
		return ImageSourceRegistry, nil
	default:
		return "", fmt.Errorf("invalid upgrade image source %q: want %q or %q",
			raw, ImageSourceReleaseAssets, ImageSourceRegistry)
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
	case string(ImageSourceReleaseAssets):
		return ImageSourceReleaseAssets, nil
	default:
		return "", fmt.Errorf("invalid upgrade image fallback %q: want \"none\", %q, or %q",
			raw, ImageSourceRegistry, ImageSourceReleaseAssets)
	}
}

// TrackImageDefaults holds edition-registered default upgrade image source
// settings for an installtrack.Track. An edition registers its defaults from
// init() via RegisterTrackDefaults so deployments on that track use release
// assets (or any custom defaults) without requiring daemon flags.
type TrackImageDefaults struct {
	Source            ImageSource
	Fallback          ImageSource
	ReleaseRepository string
	ManifestAsset     string
	Asset             string
	ChecksumAsset     string
	ChecksumSuffix    string
	SandboxAssets     map[string]string
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
	if d.SandboxAssets != nil {
		cp := make(map[string]string, len(d.SandboxAssets))
		for k, v := range d.SandboxAssets {
			cp[k] = v
		}
		d.SandboxAssets = cp
	}
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
	if !ok {
		return TrackImageDefaults{}, false
	}
	if d.SandboxAssets != nil {
		cp := make(map[string]string, len(d.SandboxAssets))
		for k, v := range d.SandboxAssets {
			cp[k] = v
		}
		d.SandboxAssets = cp
	}
	return d, true
}

// ReleaseHostClient carries the HTTP client and authentication metadata for
// fetching releases and release assets from a GitHub or GitHub Enterprise host.
type ReleaseHostClient struct {
	// Client performs HTTP requests to the forge's REST API.
	Client *http.Client
	// Token, when non-empty, is sent as `Authorization: Bearer <token>` on
	// requests to the forge's API host and stripped on cross-host redirects.
	Token string
	// Connection names the deployment git connection selected for this host,
	// if one exists, for actionable error reporting.
	Connection string
	// MissingHint names the git connection or login command an operator should
	// configure or refresh if the forge refuses release access.
	MissingHint string
	// APIBase overrides the default REST API root for this host when non-empty.
	APIBase string
}

// ReleaseAssetsConfig configures ImageSourceReleaseAssets on ImageConfig.
type ReleaseAssetsConfig struct {
	// Repository is `<host>/<owner>/<name>` whose GitHub / GitHub Enterprise
	// releases carry the image tarballs (e.g. "github.com/ai-on-gke/grain").
	Repository string
	// ManifestAsset is the release manifest filename; defaults to "images.json".
	ManifestAsset string
	// Asset is an explicit tarball asset name for the daemon ("grain") image
	// when a release has no images.json manifest or when overriding it.
	Asset string
	// ChecksumAsset is an explicit checksum asset name for Asset; when empty,
	// ChecksumSuffix (default ".sha256") is appended to the tarball asset name.
	ChecksumAsset string
	// ChecksumSuffix is appended to a tarball asset name to locate its sha256sum
	// sidecar; defaults to ".sha256".
	ChecksumSuffix string
	// SandboxAssets maps a sandbox role ("guest", "k8s-sandbox") or sandbox
	// image repository to a release tarball asset name when not supplied by
	// images.json or when overriding it.
	SandboxAssets map[string]string
	// StagingDir is a directory on a disk with enough free space to stream
	// downloaded tarballs before `docker load`. When empty, chooses between
	// filepath.Dir(RefFile) and os.TempDir().
	StagingDir string
	// APIBase overrides the GitHub REST API root ("https://api.github.com" on
	// github.com, "https://<host>/api/v3" elsewhere). Used for custom endpoints
	// and fake GitHub API servers in tests.
	APIBase string
	// ClientForHost resolves the HTTP client and git-connection credentials for
	// host. When nil, uses http.DefaultClient anonymously.
	ClientForHost func(ctx context.Context, host, owner, repo string) (*ReleaseHostClient, error)
}

// ReleaseManifest is the parsed v1 `images.json` release-asset manifest.
type ReleaseManifest struct {
	Schema     int                            `json:"schema"`
	Repository string                         `json:"repository"`
	Commit     string                         `json:"commit"`
	Version    string                         `json:"version"`
	CreatedAt  string                         `json:"createdAt"`
	Images     map[string]ReleaseManifestRole `json:"images"`
}

// ReleaseManifestRole describes one container image tarball in images.json.
type ReleaseManifestRole struct {
	Asset   string `json:"asset"`
	SHA256  string `json:"sha256"`
	ImageID string `json:"imageId"`
	Ref     string `json:"ref"`
	// Release is the tag of the release holding Asset and its sidecar
	// (schema 2); empty means the release images.json came from.
	Release string `json:"release,omitempty"`
}

type githubRelease struct {
	ID              int64                `json:"id"`
	TagName         string               `json:"tag_name"`
	TargetCommitish string               `json:"target_commitish"`
	Draft           bool                 `json:"draft"`
	Prerelease      bool                 `json:"prerelease"`
	CreatedAt       string               `json:"created_at"`
	Body            string               `json:"body"`
	Assets          []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type resolvedRelease struct {
	host       string
	owner      string
	repo       string
	apiBase    string
	hostClient *ReleaseHostClient
	tag        string
	release    githubRelease
	manifest   *ReleaseManifest
	// holders caches the releases a schema-2 manifest's images point at.
	holders map[string]*githubRelease
}

type stagedReleaseDaemon struct {
	resolved  *resolvedRelease
	stagedRef string
	targetTag string
	loadedRef string
	imageID   string
	assetName string
	assetSHA  string
	commit    string
}

type recordedReleaseImage struct {
	Ref        string `json:"ref"`
	LoadedRef  string `json:"loadedRef,omitempty"`
	ImageID    string `json:"imageId,omitempty"`
	Commit     string `json:"commit,omitempty"`
	AssetSHA   string `json:"assetSha256,omitempty"`
	ReleaseTag string `json:"releaseTag,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// statfsAvailableBytes returns free bytes available to an unprivileged user on
// dir's filesystem. Overridable for deterministic disk-space tests.
var statfsAvailableBytes = func(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	if st.Bsize <= 0 {
		return 0, fmt.Errorf("invalid filesystem block size %d for %s", st.Bsize, dir)
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// ParseReleaseRepository splits "<host>/<owner>/<name>" into its three parts.
func ParseReleaseRepository(raw string) (host, owner, repo string, err error) {
	s := strings.TrimSpace(strings.TrimSuffix(raw, ".git"))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	parts := strings.Split(s, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || !strings.Contains(parts[0], ".") && !strings.Contains(parts[0], ":") && parts[0] != "localhost" {
		return "", "", "", fmt.Errorf("invalid release repository %q: want <host>/<owner>/<name> (e.g. github.com/ai-on-gke/grain)", raw)
	}
	return parts[0], parts[1], parts[2], nil
}

func releaseAPIBase(host, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimRight(strings.TrimSpace(override), "/")
	}
	if strings.EqualFold(hostNameOnly(host), "github.com") {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

func hostNameOnly(h string) string {
	if idx := strings.LastIndex(h, ":"); idx != -1 && !strings.Contains(h[idx:], "]") {
		return h[:idx]
	}
	return h
}

// ReleaseTagsForBranch returns the GitHub release tag candidate(s) for a target
// branch, channel, or version, ordered from preferred to legacy fallback.
//
// Moving branch channels map to `channel-<relTag>` (for example `channel-main`,
// `channel-candidate`, and `channel-rc-v3.2`), avoiding the git ref ambiguity
// created when a release tag named `main` shadows branch `main`. Slashes in
// branch names map to `-` rather than TagForBranch's `..` because GitHub / GHES
// release tags are backed by git refs (`refs/tags/<tag>`), where
// `git-check-ref-format` forbids `..`; local Docker tags still use
// TagForBranch's `..` so ChannelForRef can invert them. If `channel-<relTag>`
// is 404 on a repository still using pre-channel tag names, the caller may
// query `<relTag>` through the same REST `/releases/tags/<tag>` endpoint (never
// via git ref resolution).
func ReleaseTagsForBranch(branch string) ([]string, error) {
	branch = strings.TrimSpace(branch)
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	if branch == "latest" || versionTag.MatchString(branch) || candidateTag.MatchString(branch) || strings.HasPrefix(branch, "sha-") {
		return []string{branch}, nil
	}
	if branch == "rc" {
		return []string{"channel-candidate", "candidate"}, nil
	}
	tag := TagForBranch(branch)
	if len(tag) > maxTagLen {
		return nil, fmt.Errorf("branch %q makes a %d-character tag, over the %d-character limit", branch, len(tag), maxTagLen)
	}
	if !strings.Contains(branch, "/") && strings.HasPrefix(branch, "channel-") {
		return []string{branch}, nil
	}
	relTag := strings.ReplaceAll(branch, "/", "-")
	return []string{"channel-" + relTag, relTag}, nil
}

// ReleaseRepositoryForImage maps a container image repository to the GitHub
// repository (`github.com/<owner>/<name>`) whose releases publish its assets,
// or returns "" when no GitHub repository can be inferred from imageRepo.
func ReleaseRepositoryForImage(imageRepo string) string {
	repo := strings.TrimSpace(repositoryOf(strings.TrimSpace(imageRepo)))
	if repo == installtrack.DefaultOSSGrainRepository ||
		repo == "index.docker.io/grainai/grain" ||
		repo == "registry-1.docker.io/grainai/grain" ||
		repo == "grainai/grain" ||
		repo == installtrack.LegacyGrainRepository ||
		repo == "ghcr.io/ai-on-gke/grain/grain" ||
		repo == "ghcr.io/ai-on-gke/grain" {
		return "github.com/ai-on-gke/grain"
	}
	if rest, ok := strings.CutPrefix(repo, "ghcr.io/"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return "github.com/" + parts[0] + "/" + parts[1]
		}
	}
	return ""
}

// IsImageSourceExplicit reports whether the deployment's image source was
// explicitly pinned (via ImageConfig.Source or TrackImageDefaults.Source) rather
// than using core's default release-assets-with-missing-manifest-fallback behavior.
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

// EffectiveImageSource returns the active image source for ModeImage (explicit
// ImageConfig.Source, else registered TrackImageDefaults, else release-assets
// -- or registry on a deployment with a VerifyCmd, whose signatures are over
// registry manifests and pass a loaded tarball only when its image ID is a
// signed digest (verifyLoaded), so that it is not moved onto a source its
// verifier may refuse every upgrade from).
func (u *Upgrader) EffectiveImageSource() ImageSource {
	if u.cfg.Image == nil {
		return ImageSourceRegistry
	}
	if s := strings.TrimSpace(string(u.cfg.Image.Source)); s != "" {
		if norm, err := NormalizeImageSource(s); err == nil {
			return norm
		}
		return u.cfg.Image.Source
	}
	if d, ok := TrackDefaultsFor(u.cfg.Image.Track); ok && d.Source != "" {
		return d.Source
	}
	if len(u.cfg.Image.VerifyCmd) > 0 {
		return ImageSourceRegistry
	}
	return ImageSourceReleaseAssets
}

// EffectiveImageFallback returns the configured fallback image source for
// ModeImage, or "" when fallback is disabled or matches the primary source.
// When the image source was left at its default (neither explicit `registry`
// nor explicit `release-assets`), fallback defaults to `registry` when the
// target release has no image assets (unless FallbackExplicit disabled it).
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
	} else if !u.IsImageSourceExplicit() && primary == ImageSourceReleaseAssets {
		fb = ImageSourceRegistry
	}
	if fb == primary {
		return ""
	}
	return fb
}

// shouldBypassDefaultReleaseAssets reports whether an unpinned (default-source)
// Upgrader should go straight to the registry path without dialing a GitHub API:
// either because the configured image repository does not map to any release
// repository, or because an offline unit test injected registry test seams
// (PullCmd / ManifestDigest / ListTags) without configuring ClientForHost or a
// release repository.
func (u *Upgrader) shouldBypassDefaultReleaseAssets() bool {
	if u.cfg.Image == nil || u.IsImageSourceExplicit() {
		return false
	}
	ra := u.EffectiveReleaseAssets()
	if strings.TrimSpace(ra.Repository) == "" {
		return true
	}
	if ra.ClientForHost == nil &&
		strings.TrimSpace(u.cfg.Image.ReleaseAssets.Repository) == "" &&
		strings.TrimSpace(u.cfg.Image.ReleaseAssets.APIBase) == "" &&
		(len(u.cfg.Image.PullCmd) > 0 || u.cfg.ManifestDigest != nil || u.cfg.ListTags != nil) {
		return true
	}
	return false
}

// shouldFallbackToRegistry decides whether a release-assets error err should
// trigger fallback to a container registry pull, and returns the operator-facing
// fallback reason string when true.
//
//   - Under an explicit `-upgrade-image-fallback=registry` (FallbackExplicit or
//     explicit Fallback field), any release-assets fetch failure falls back,
//     except integrity/signature failures after an asset or manifest was read.
//   - Under the default image source (`!IsImageSourceExplicit()`), fallback
//     triggers specifically when the target release does not exist or carries
//     no `images.json` asset (`errNoReleaseImageAssets`), reporting
//     `"release has no image assets; pulled from <registry>"`.
func (u *Upgrader) shouldFallbackToRegistry(err error) (bool, string) {
	if err == nil || u.EffectiveImageFallback() != ImageSourceRegistry {
		return false, ""
	}
	repo := u.EffectiveRepository()
	if errors.Is(err, errNoReleaseImageAssets) {
		return true, fmt.Sprintf("release has no image assets; pulled from %s", repo)
	}
	// If the source is unpinned (default) and no explicit fallback was set,
	// only missing releases / missing images.json fall back automatically;
	// auth errors, malformed manifests, or checksum mismatches surface directly.
	if !u.IsImageSourceExplicit() && !u.cfg.Image.FallbackExplicit && strings.TrimSpace(string(u.cfg.Image.Fallback)) == "" {
		return false, ""
	}
	// Never mask an integrity mismatch, or a moving release caught between
	// its publisher's uploads, even when explicit fallback is enabled.
	if errors.Is(err, errReleaseIntegrity) || errors.Is(err, errReleaseMidUpdate) {
		return false, ""
	}
	return true, fmt.Sprintf("release-assets unavailable (%v); pulled from %s", err, repo)
}

// EffectiveReleaseAssets returns the merged ReleaseAssetsConfig after applying
// track defaults, image-to-GitHub repository mapping, and standard manifest/checksum defaults.
func (u *Upgrader) EffectiveReleaseAssets() ReleaseAssetsConfig {
	if u.cfg.Image == nil {
		return ReleaseAssetsConfig{}
	}
	out := u.cfg.Image.ReleaseAssets
	if d, ok := TrackDefaultsFor(u.cfg.Image.Track); ok {
		if strings.TrimSpace(out.Repository) == "" {
			out.Repository = d.ReleaseRepository
		}
		if strings.TrimSpace(out.ManifestAsset) == "" {
			out.ManifestAsset = d.ManifestAsset
		}
		if strings.TrimSpace(out.Asset) == "" {
			out.Asset = d.Asset
		}
		if strings.TrimSpace(out.ChecksumAsset) == "" {
			out.ChecksumAsset = d.ChecksumAsset
		}
		if strings.TrimSpace(out.ChecksumSuffix) == "" {
			out.ChecksumSuffix = d.ChecksumSuffix
		}
		if len(d.SandboxAssets) > 0 {
			merged := make(map[string]string, len(d.SandboxAssets)+len(out.SandboxAssets))
			for k, v := range d.SandboxAssets {
				merged[k] = v
			}
			for k, v := range out.SandboxAssets {
				merged[k] = v
			}
			out.SandboxAssets = merged
		}
	}
	if strings.TrimSpace(out.Repository) == "" {
		out.Repository = ReleaseRepositoryForImage(u.EffectiveRepository())
	}
	if strings.TrimSpace(out.ManifestAsset) == "" {
		out.ManifestAsset = DefaultReleaseManifestAsset
	}
	if strings.TrimSpace(out.ChecksumSuffix) == "" {
		out.ChecksumSuffix = DefaultChecksumSuffix
	}
	return out
}

// ParseReleaseManifest parses and validates a v1 `images.json` payload.
// Unknown top-level fields and unknown role keys are ignored; unsupported
// schema versions or missing/malformed fields on known roles are refused.
func ParseReleaseManifest(raw []byte) (*ReleaseManifest, error) {
	var probe struct {
		Schema *int `json:"schema"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("parsing images.json: %w", err)
	}
	if probe.Schema == nil {
		return nil, errors.New("invalid images.json: missing required \"schema\" field")
	}
	if *probe.Schema != ReleaseManifestSchemaV1 && *probe.Schema != ReleaseManifestSchemaV2 {
		return nil, fmt.Errorf("unsupported images.json schema %d (supported: %d, %d)", *probe.Schema, ReleaseManifestSchemaV1, ReleaseManifestSchemaV2)
	}
	var m ReleaseManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing images.json: %w", err)
	}
	if strings.TrimSpace(m.Repository) == "" {
		return nil, errors.New("invalid images.json: missing required \"repository\" field")
	}
	m.Commit = strings.ToLower(strings.TrimSpace(m.Commit))
	if !hex40Pattern.MatchString(m.Commit) {
		return nil, fmt.Errorf("invalid images.json: \"commit\" %q must be a 40-character hex commit SHA", m.Commit)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, errors.New("invalid images.json: missing required \"version\" field")
	}
	if strings.TrimSpace(m.CreatedAt) == "" {
		return nil, errors.New("invalid images.json: missing required \"createdAt\" field")
	}
	if _, err := time.Parse(time.RFC3339, m.CreatedAt); err != nil {
		return nil, fmt.Errorf("invalid images.json: \"createdAt\" %q is not RFC 3339: %w", m.CreatedAt, err)
	}
	if len(m.Images) == 0 {
		return nil, errors.New("invalid images.json: \"images\" contains no entries")
	}
	knownRoles := map[string]bool{
		ReleaseRoleGrain:      true,
		ReleaseRoleGuest:      true,
		ReleaseRoleK8sSandbox: true,
	}
	normalized := make(map[string]ReleaseManifestRole, len(knownRoles))
	for role, entry := range m.Images {
		if !knownRoles[role] {
			continue
		}
		if err := validateManifestRole(role, &entry); err != nil {
			return nil, err
		}
		if m.Schema == ReleaseManifestSchemaV1 {
			entry.Release = ""
		} else if entry.Release = strings.TrimSpace(entry.Release); entry.Release == "" || isMovingReleaseTag(entry.Release) ||
			validateBranch(entry.Release) != nil {
			return nil, fmt.Errorf("invalid images.json role %q: \"release\" %q must name an immutable sha-<short> or vX.Y.Z release", role, entry.Release)
		}
		normalized[role] = entry
	}
	m.Images = normalized
	return &m, nil
}

func validateManifestRole(role string, entry *ReleaseManifestRole) error {
	entry.Asset = strings.TrimSpace(entry.Asset)
	if entry.Asset == "" || entry.Asset == "." || entry.Asset == ".." || strings.ContainsAny(entry.Asset, `/\`) {
		return fmt.Errorf("invalid images.json role %q: invalid asset filename %q", role, entry.Asset)
	}
	entry.SHA256 = strings.ToLower(strings.TrimSpace(entry.SHA256))
	if !hex64Pattern.MatchString(entry.SHA256) {
		return fmt.Errorf("invalid images.json role %q: sha256 %q must be 64 hex characters", role, entry.SHA256)
	}
	entry.ImageID = strings.ToLower(strings.TrimSpace(entry.ImageID))
	if !fullImageIDRegex.MatchString(entry.ImageID) {
		return fmt.Errorf("invalid images.json role %q: imageId %q must be sha256:<64-hex>", role, entry.ImageID)
	}
	entry.Ref = strings.TrimSpace(entry.Ref)
	if entry.Ref == "" || len(entry.Ref) > 512 || !imageRefPattern.MatchString(entry.Ref) {
		return fmt.Errorf("invalid images.json role %q: invalid image ref %q", role, entry.Ref)
	}
	return nil
}

func parseSHA256Sidecar(raw, expectedAsset string) (string, error) {
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		digest := strings.ToLower(fields[0])
		if !hex64Pattern.MatchString(digest) {
			return "", fmt.Errorf("malformed sha256 checksum %q", line)
		}
		if len(fields) >= 2 && expectedAsset != "" {
			name := strings.TrimPrefix(fields[1], "*")
			if filepath.Base(name) != expectedAsset && name != expectedAsset {
				continue
			}
		}
		return digest, nil
	}
	return "", fmt.Errorf("no valid sha256 digest found for %s", expectedAsset)
}

// ParseDockerLoadOutput extracts the loaded image reference (`Loaded image: ...`)
// and/or loaded image ID (`Loaded image ID: sha256:...`) printed by `docker load`.
func ParseDockerLoadOutput(out string) (loadedRef, loadedID string, err error) {
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if rest, ok := strings.CutPrefix(line, "Loaded image:"); ok {
			if ref := strings.TrimSpace(rest); ref != "" {
				loadedRef = ref
			}
		} else if rest, ok := strings.CutPrefix(line, "Loaded image ID:"); ok {
			if id := strings.ToLower(strings.TrimSpace(rest)); id != "" {
				if !strings.HasPrefix(id, "sha256:") && hex64Pattern.MatchString(id) {
					id = "sha256:" + id
				}
				loadedID = id
			}
		}
	}
	if loadedRef == "" && loadedID == "" {
		return "", "", fmt.Errorf("could not parse loaded image reference or ID from `docker load` output: %q", truncate(strings.TrimSpace(out), 200))
	}
	return loadedRef, loadedID, nil
}

func (u *Upgrader) resolveReleaseClient(ctx context.Context, cfg ReleaseAssetsConfig) (host, owner, repo, apiBase string, hc *ReleaseHostClient, err error) {
	host, owner, repo, err = ParseReleaseRepository(cfg.Repository)
	if err != nil {
		return "", "", "", "", nil, err
	}
	if cfg.ClientForHost != nil {
		hc, err = cfg.ClientForHost(ctx, host, owner, repo)
		if err != nil {
			return "", "", "", "", nil, err
		}
	}
	if hc == nil {
		hc = &ReleaseHostClient{Client: http.DefaultClient}
	}
	if hc.Client == nil {
		hc.Client = http.DefaultClient
	}
	baseOverride := cfg.APIBase
	if baseOverride == "" {
		baseOverride = hc.APIBase
	}
	apiBase = releaseAPIBase(host, baseOverride)
	return host, owner, repo, apiBase, hc, nil
}

// clientWithRedirectAuthGuard returns an *http.Client that strips Authorization
// headers whenever a redirect crosses to another host (such as signed blob
// storage URLs for release assets).
func clientWithRedirectAuthGuard(base *http.Client, timeout time.Duration) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	cp := *base
	if timeout > 0 {
		cp.Timeout = timeout
	}
	prevRedirect := base.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			req.Header.Del("Authorization")
			// Signed object-storage URLs (S3/GCS/Fastly) expect normal binary GET
			// without GitHub API media-type negotiation headers.
			req.Header.Del("Accept")
		}
		if prevRedirect != nil {
			return prevRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &cp
}

func formatReleaseAuthError(host, owner, repo string, status int, cause error, hc *ReleaseHostClient) error {
	hint := ""
	if hc != nil && hc.MissingHint != "" {
		hint = hc.MissingHint
	} else if hc != nil && hc.Connection != "" {
		hint = fmt.Sprintf("check git connection %q for %s in Settings -> Connections", hc.Connection, host)
	} else {
		hint = fmt.Sprintf("configure a git connection for %s in Settings -> Connections", host)
	}
	if cause != nil {
		if login, ok := gitproxy.AsLoginRedirect(cause); ok {
			return fmt.Errorf("fetching releases from %s/%s/%s redirected to login at %s -- %s",
				host, owner, repo, login.Location, hint)
		}
		return fmt.Errorf("fetching releases from %s/%s/%s: %w -- %s", host, owner, repo, cause, hint)
	}
	return fmt.Errorf("fetching releases from %s/%s/%s answered HTTP %d -- %s",
		host, owner, repo, status, hint)
}

// authorize sends the release host's token with req only when req goes
// to that host's own API: an asset's URL is read from the API's answer,
// and the token is never to follow it anywhere else.
func (r *resolvedRelease) authorize(req *http.Request) {
	if r.hostClient == nil || r.hostClient.Token == "" {
		return
	}
	base, err := url.Parse(r.apiBase)
	if err != nil || !strings.EqualFold(base.Host, req.URL.Host) {
		return
	}
	req.Header.Set("Authorization", "Bearer "+r.hostClient.Token)
}

func (r *resolvedRelease) doAPI(ctx context.Context, targetURL, accept string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, 0, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	r.authorize(req)
	client := clientWithRedirectAuthGuard(r.hostClient.Client, releaseMetadataTimeout)
	resp, err := client.Do(req)
	if err != nil {
		if _, ok := gitproxy.AsLoginRedirect(err); ok {
			return nil, 0, formatReleaseAuthError(r.host, r.owner, r.repo, 0, err, r.hostClient)
		}
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if readErr != nil {
		return nil, resp.StatusCode, readErr
	}
	return body, resp.StatusCode, nil
}

func (u *Upgrader) resolveRelease(ctx context.Context, branch string) (*resolvedRelease, error) {
	cfg := u.EffectiveReleaseAssets()
	if strings.TrimSpace(cfg.Repository) == "" {
		return nil, errors.New("upgrade image source is release-assets, but no release repository is configured (-upgrade-release-repo)")
	}
	candidates, err := ReleaseTagsForBranch(branch)
	if err != nil {
		return nil, err
	}
	host, owner, repo, apiBase, hc, err := u.resolveReleaseClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	res := &resolvedRelease{
		host:       host,
		owner:      owner,
		repo:       repo,
		apiBase:    apiBase,
		hostClient: hc,
	}

	var found bool
	// moving is whether branch names a release re-pointed in place
	// (channel-<branch>, latest) rather than one published once; only a
	// moving target may resolve to another release when its own has
	// nothing to load.
	moving := isMovingReleaseTag(candidates[0])
	for _, tag := range candidates {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s",
			apiBase, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tag))
		if tag == "latest" {
			// GitHub's own latest-release marker, never a release (or git
			// tag) named latest (contract v2).
			endpoint = fmt.Sprintf("%s/repos/%s/%s/releases/latest", apiBase, url.PathEscape(owner), url.PathEscape(repo))
		}
		body, status, reqErr := res.doAPI(ctx, endpoint, "application/vnd.github+json")
		if reqErr != nil {
			return nil, reqErr
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, formatReleaseAuthError(host, owner, repo, status, nil, hc)
		}
		if status == http.StatusNotFound {
			// On private repositories, GitHub returns 404 when no credential (or a
			// credential without read access) was supplied. If this was the last
			// candidate tag and no token or ambient CLI transport was active,
			// surface the connection hint alongside the 404.
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("looking up release %q on %s/%s/%s: HTTP %d: %s",
				tag, host, owner, repo, status, truncate(strings.TrimSpace(string(body)), 200))
		}
		var rel githubRelease
		if err := json.Unmarshal(body, &rel); err != nil {
			return nil, fmt.Errorf("parsing release %q on %s/%s/%s: %w", tag, host, owner, repo, err)
		}
		res.tag = tag
		if tag == "latest" && strings.TrimSpace(rel.TagName) != "" {
			res.tag = rel.TagName
		}
		res.release = rel
		found = true
		break
	}
	requiredAsset := requiredReleaseAsset(cfg)
	if !found {
		tried := strings.Join(candidates, ", ")
		var notFound error
		if hc != nil && hc.Token == "" && !usesCommandForge(hc.Client) && hc.MissingHint != "" {
			notFound = fmt.Errorf("%w: release %s not found on %s/%s/%s (HTTP 404; if the repository is private, %s)",
				errNoReleaseImageAssets, tried, host, owner, repo, hc.MissingHint)
		} else {
			notFound = fmt.Errorf("%w: no release tagged %s on %s/%s/%s", errNoReleaseImageAssets, tried, host, owner, repo)
		}
		if !u.releaseAssetsOnly() {
			return nil, notFound
		}
		newest, ok := u.newestPublishedRelease(ctx, res, cfg, candidates[0])
		if !ok {
			return nil, notFound
		}
		if !moving {
			return nil, notPublishedError(candidates[0], res, newest)
		}
		res.tag, res.release = newest.TagName, newest
	} else if _, ok := res.findAsset(requiredAsset); !ok {
		// A release with no manifest (or configured single asset): a moving
		// one mid-swap (images.json.next is up and images.json not yet
		// back), or a release that was tagged without being published. A
		// deployment that loads release assets and nothing else stays on
		// the last release that was published.
		swapping := strings.TrimSpace(cfg.Asset) == "" && isMovingReleaseTag(res.tag) && countAssets(res.release, cfg.ManifestAsset+".next") > 0
		if !swapping && u.releaseAssetsOnly() {
			if newest, ok := u.newestPublishedRelease(ctx, res, cfg, candidates[0]); ok {
				if !moving {
					return nil, notPublishedError(res.tag, res, newest)
				}
				res.tag, res.release = newest.TagName, newest
			}
		}
	}

	// Load images.json when present on the release unless an explicit single
	// Asset override bypasses it.
	if n := countAssets(res.release, cfg.ManifestAsset); n > 1 && isMovingReleaseTag(res.tag) {
		return nil, fmt.Errorf("%w: release %s on %s/%s/%s carries %d %s assets (its publisher is swapping them); try again later",
			errReleaseMidUpdate, res.tag, host, owner, repo, n, cfg.ManifestAsset)
	}
	if asset, ok := res.findAsset(cfg.ManifestAsset); ok {
		raw, err := res.fetchSmallAsset(ctx, asset)
		if err != nil {
			return nil, fmt.Errorf("fetching %s from release %s on %s/%s/%s: %w",
				asset.Name, res.tag, host, owner, repo, err)
		}
		manifest, err := ParseReleaseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: release %s on %s/%s/%s: %w", errReleaseIntegrity, res.tag, host, owner, repo, err)
		}
		res.manifest = manifest
	} else if strings.TrimSpace(cfg.Asset) == "" {
		if isMovingReleaseTag(res.tag) && !releaseSaysNotPublished(res.release) {
			return nil, fmt.Errorf("%w: release %s on %s/%s/%s has no %s yet (its publisher uploads it last); try again later",
				errReleaseMidUpdate, res.tag, host, owner, repo, cfg.ManifestAsset)
		}
		return nil, fmt.Errorf("%w: release %s on %s/%s/%s has no %s manifest asset (and no -upgrade-release-asset configured)",
			errNoReleaseImageAssets, res.tag, host, owner, repo, cfg.ManifestAsset)
	}

	return res, nil
}

// isMovingReleaseTag reports whether tag names a release that is re-pointed
// in place (channel-<branch>, latest, or a pre-channel bare branch tag)
// rather than one published once (sha-<short>, vX.Y.Z, release candidates).
func isMovingReleaseTag(tag string) bool {
	if tag == "latest" || strings.HasPrefix(tag, "channel-") {
		return true
	}
	return !strings.HasPrefix(tag, "sha-") && !versionTag.MatchString(tag) && !candidateTag.MatchString(tag)
}

// notPublishedMarker is what the publisher writes in the body of a release
// it tagged without uploading image assets (asset publishing switched off).
const notPublishedMarker = "tagged, not published"

// releaseSaysNotPublished reports whether rel's body carries the
// publisher's "tagged, not published" marker.
func releaseSaysNotPublished(rel githubRelease) bool {
	return strings.Contains(strings.ToLower(rel.Body), notPublishedMarker)
}

// releaseAssetsOnly reports whether this deployment loads images from
// release assets with no registry to fall back to, so that a release
// without image assets can only be skipped (or refused), never pulled
// some other way.
func (u *Upgrader) releaseAssetsOnly() bool {
	return u.cfg.Image != nil && u.EffectiveImageSource() == ImageSourceReleaseAssets && u.EffectiveImageFallback() == ""
}

// maxReleasePages bounds how many pages of 100 releases a listing reads.
const maxReleasePages = 10

// listReleases pages through GET /repos/{owner}/{repo}/releases, newest
// first as the forge answers, reading at most maxReleasePages pages.
// stop, when non-nil, is asked after each page whether what has been read
// so far is enough.
func (r *resolvedRelease) listReleases(ctx context.Context, stop func([]githubRelease) bool) ([]githubRelease, error) {
	var all []githubRelease
	for page := 1; page <= maxReleasePages; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100&page=%d",
			r.apiBase, url.PathEscape(r.owner), url.PathEscape(r.repo), page)
		body, status, err := r.doAPI(ctx, endpoint, "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, formatReleaseAuthError(r.host, r.owner, r.repo, status, nil, r.hostClient)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("listing releases on %s/%s/%s: HTTP %d: %s",
				r.host, r.owner, r.repo, status, truncate(strings.TrimSpace(string(body)), 200))
		}
		var batch []githubRelease
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("parsing releases on %s/%s/%s: %w", r.host, r.owner, r.repo, err)
		}
		all = append(all, batch...)
		if len(batch) < 100 || (stop != nil && stop(all)) {
			break
		}
	}
	return all, nil
}

// publishedReleases filters releases to the ones an upgrade can load: not
// drafts, not moving pointers (channel-<branch>, latest, a legacy bare
// branch tag), and carrying manifest -- sorted newest first by created_at,
// then id.
func publishedReleases(releases []githubRelease, manifest string) []githubRelease {
	var out []githubRelease
	for _, rel := range releases {
		tag := strings.TrimSpace(rel.TagName)
		if rel.Draft || tag == "" || isMovingReleaseTag(tag) || countAssets(rel, manifest) == 0 {
			continue
		}
		out = append(out, rel)
	}
	slices.SortStableFunc(out, func(a, b githubRelease) int {
		ta, errA := time.Parse(time.RFC3339, strings.TrimSpace(a.CreatedAt))
		tb, errB := time.Parse(time.RFC3339, strings.TrimSpace(b.CreatedAt))
		switch {
		case errA == nil && errB == nil && !ta.Equal(tb):
			return tb.Compare(ta)
		case errA != nil && errB == nil:
			return 1
		case errA == nil && errB != nil:
			return -1
		}
		switch {
		case a.ID > b.ID:
			return -1
		case a.ID < b.ID:
			return 1
		}
		return 0
	})
	return out
}

// pickPublishedRelease returns the newest of published (already sorted
// newest first): the newest non-prerelease vX.Y.Z when preferVersion and
// there is one, else the newest of all.
func pickPublishedRelease(published []githubRelease, preferVersion bool) (githubRelease, bool) {
	if len(published) == 0 {
		return githubRelease{}, false
	}
	if preferVersion {
		for _, rel := range published {
			if !rel.Prerelease && versionTag.MatchString(strings.TrimSpace(rel.TagName)) {
				return rel, true
			}
		}
	}
	return published[0], true
}

// requiredReleaseAsset returns the asset name a release must carry to count as
// published on this deployment: the explicit single-tarball Asset override when
// set, otherwise ManifestAsset (images.json).
func requiredReleaseAsset(cfg ReleaseAssetsConfig) string {
	if explicit := strings.TrimSpace(cfg.Asset); explicit != "" {
		return explicit
	}
	return cfg.ManifestAsset
}

// newestPublishedRelease is the release a target with nothing to load
// falls back to (a moving one) or is refused in favour of (an immutable
// one): the newest published release on the repository, preferring a
// promoted, non-prerelease vX.Y.Z when target is latest or itself a
// version. false when the releases cannot be listed or none is published.
func (u *Upgrader) newestPublishedRelease(ctx context.Context, res *resolvedRelease, cfg ReleaseAssetsConfig, target string) (githubRelease, bool) {
	required := requiredReleaseAsset(cfg)
	preferVersion := target == "latest" || versionTag.MatchString(target)
	releases, err := res.listReleases(ctx, func(read []githubRelease) bool {
		// The forge lists newest first: stop paging once what has been
		// read holds the release this would pick.
		rel, ok := pickPublishedRelease(publishedReleases(read, required), preferVersion)
		return ok && (!preferVersion || !rel.Prerelease && versionTag.MatchString(rel.TagName))
	})
	if err != nil {
		log.Printf("upgrade: listing published releases on %s/%s/%s: %v", res.host, res.owner, res.repo, err)
		return githubRelease{}, false
	}
	return pickPublishedRelease(publishedReleases(releases, required), preferVersion)
}

// notPublishedError refuses an immutable release that carries no image
// assets, naming the newest one that does. It matches both
// errReleaseNotPublished and errNoReleaseImageAssets under errors.Is.
func notPublishedError(tag string, res *resolvedRelease, newest githubRelease) error {
	return &releaseNotPublishedError{msg: fmt.Sprintf("release %s on %s/%s/%s is not published; the newest published release is %s",
		tag, res.host, res.owner, res.repo, newest.TagName)}
}

type releaseNotPublishedError struct{ msg string }

func (e *releaseNotPublishedError) Error() string { return e.msg }

func (e *releaseNotPublishedError) Is(target error) bool {
	return target == errReleaseNotPublished || target == errNoReleaseImageAssets
}

func usesCommandForge(c *http.Client) bool {
	if c == nil {
		return false
	}
	_, ok := c.Transport.(*gitproxy.CommandForgeTransport)
	return ok
}

func countAssets(rel githubRelease, name string) int {
	n := 0
	for _, a := range rel.Assets {
		if a.Name == name {
			n++
		}
	}
	return n
}

// holder returns the release a manifest image's asset lives on: tag
// itself, or (schema 2) the immutable release it names, looked up by
// tag through the releases API and cached.
func (r *resolvedRelease) holder(ctx context.Context, tag string) (*resolvedRelease, error) {
	if tag == "" || tag == r.tag {
		return r, nil
	}
	if rel, ok := r.holders[tag]; ok {
		cp := *r
		cp.tag, cp.release = tag, *rel
		return &cp, nil
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s",
		r.apiBase, url.PathEscape(r.owner), url.PathEscape(r.repo), url.PathEscape(tag))
	body, status, err := r.doAPI(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, formatReleaseAuthError(r.host, r.owner, r.repo, status, nil, r.hostClient)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("release %s on %s/%s/%s names release %s for its images, which answered HTTP %d",
			r.tag, r.host, r.owner, r.repo, tag, status)
	}
	var rel githubRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("parsing release %q on %s/%s/%s: %w", tag, r.host, r.owner, r.repo, err)
	}
	if r.holders == nil {
		r.holders = map[string]*githubRelease{}
	}
	r.holders[tag] = &rel
	cp := *r
	cp.tag, cp.release = tag, rel
	return &cp, nil
}

func (r *resolvedRelease) findAsset(name string) (githubReleaseAsset, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return githubReleaseAsset{}, false
	}
	for _, a := range r.release.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return githubReleaseAsset{}, false
}

func (r *resolvedRelease) assetDownloadURL(asset githubReleaseAsset) string {
	if strings.TrimSpace(asset.URL) != "" {
		return asset.URL
	}
	return fmt.Sprintf("%s/repos/%s/%s/releases/assets/%d",
		r.apiBase, url.PathEscape(r.owner), url.PathEscape(r.repo), asset.ID)
}

func (r *resolvedRelease) fetchSmallAsset(ctx context.Context, asset githubReleaseAsset) ([]byte, error) {
	targetURL := r.assetDownloadURL(asset)
	body, status, err := r.doAPI(ctx, targetURL, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, formatReleaseAuthError(r.host, r.owner, r.repo, status, nil, r.hostClient)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("downloading asset %s answered HTTP %d: %s",
			asset.Name, status, truncate(strings.TrimSpace(string(body)), 200))
	}
	return body, nil
}

func (r *resolvedRelease) expectedAssetSpec(cfg ReleaseAssetsConfig, role, explicitAsset, explicitChecksum string) (tarball githubReleaseAsset, expectedSHA, expectedImageID, expectedRef string, err error) {
	var useManifestRole *ReleaseManifestRole
	if r.manifest != nil {
		if mr, ok := r.manifest.Images[role]; ok {
			if explicitAsset == "" || explicitAsset == mr.Asset {
				cp := mr
				useManifestRole = &cp
			}
		}
	}

	assetName := strings.TrimSpace(explicitAsset)
	if useManifestRole != nil {
		assetName = useManifestRole.Asset
		expectedSHA = useManifestRole.SHA256
		expectedImageID = useManifestRole.ImageID
		expectedRef = useManifestRole.Ref
	}
	if assetName == "" {
		return githubReleaseAsset{}, "", "", "", fmt.Errorf("release %s on %s/%s/%s does not list role %q in %s and has no configured asset name",
			r.tag, r.host, r.owner, r.repo, role, cfg.ManifestAsset)
	}
	if useManifestRole != nil {
		h, err := r.holder(context.Background(), useManifestRole.Release)
		if err != nil {
			return githubReleaseAsset{}, "", "", "", err
		}
		r = h
	}

	tarball, ok := r.findAsset(assetName)
	if !ok {
		return githubReleaseAsset{}, "", "", "", fmt.Errorf("release %s on %s/%s/%s is missing asset %q",
			r.tag, r.host, r.owner, r.repo, assetName)
	}

	checksumName := strings.TrimSpace(explicitChecksum)
	if checksumName == "" {
		checksumName = assetName + cfg.ChecksumSuffix
	}
	sidecarAsset, ok := r.findAsset(checksumName)
	if !ok {
		return githubReleaseAsset{}, "", "", "", fmt.Errorf("%w: release %s on %s/%s/%s is missing checksum asset %q for %q",
			errReleaseIntegrity, r.tag, r.host, r.owner, r.repo, checksumName, assetName)
	}
	sidecarRaw, err := r.fetchSmallAsset(context.Background(), sidecarAsset)
	if err != nil {
		return githubReleaseAsset{}, "", "", "", fmt.Errorf("fetching checksum asset %q: %w", checksumName, err)
	}
	sidecarSHA, err := parseSHA256Sidecar(string(sidecarRaw), assetName)
	if err != nil {
		return githubReleaseAsset{}, "", "", "", fmt.Errorf("%w: checksum asset %q: %w", errReleaseIntegrity, checksumName, err)
	}
	if expectedSHA != "" && sidecarSHA != expectedSHA {
		return githubReleaseAsset{}, "", "", "", fmt.Errorf("%w: checksum mismatch for %q on release %s: %s says %s, %s says %s",
			errReleaseIntegrity, assetName, r.tag, cfg.ManifestAsset, expectedSHA, checksumName, sidecarSHA)
	}
	return tarball, sidecarSHA, expectedImageID, expectedRef, nil
}

func (u *Upgrader) chooseStagingDir(cfg ReleaseAssetsConfig, assetSize int64) (string, error) {
	need := uint64(0)
	if assetSize > 0 {
		need = uint64(assetSize) + minStagingHeadroom
	}
	var candidates []string
	if dir := strings.TrimSpace(cfg.StagingDir); dir != "" {
		candidates = append(candidates, dir)
	} else {
		if u.cfg.Image != nil && u.cfg.Image.RefFile != "" {
			candidates = append(candidates, filepath.Dir(u.cfg.Image.RefFile))
		}
		candidates = append(candidates, os.TempDir())
	}

	var checked []string
	for _, dir := range candidates {
		if dir == "" || slices.Contains(checked, dir) {
			continue
		}
		checked = append(checked, dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		avail, err := statfsAvailableBytes(dir)
		if err != nil {
			continue
		}
		if need == 0 || avail >= need {
			return dir, nil
		}
	}
	if len(checked) > 0 && need > 0 {
		avail, _ := statfsAvailableBytes(checked[0])
		return "", fmt.Errorf("insufficient disk space in %s to stage release asset (%d MiB needed including headroom, %d MiB available)",
			checked[0], need>>20, avail>>20)
	}
	return "", errors.New("no writable staging directory available for release asset download")
}

func (u *Upgrader) streamAndVerifyAsset(ctx context.Context, p *progress, step string, r *resolvedRelease, cfg ReleaseAssetsConfig, asset githubReleaseAsset, expectedSHA string) (string, error) {
	dir, err := u.chooseStagingDir(cfg, asset.Size)
	if err != nil {
		return "", err
	}
	// The empty placeholder DownloadToFile streams into: it fills only a
	// file that already exists, so the host's host-git server, beside a
	// containerised daemon, writes nowhere the daemon could not.
	f, err := os.CreateTemp(dir, "grain-release-asset-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("creating staging file in %s: %w", dir, err)
	}
	tmpPath := f.Name()
	_ = f.Close()

	targetURL := r.assetDownloadURL(asset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	r.authorize(req)
	if p != nil {
		p.logf("%s: streaming %s from %s/%s/%s release %s", step, asset.Name, r.host, r.owner, r.repo, r.tag)
	}

	dlClient := clientWithRedirectAuthGuard(r.hostClient.Client, releaseDownloadTimeout)
	if usesCommandForge(r.hostClient.Client) {
		dlClient = r.hostClient.Client
	}
	gotSHA, _, status, errBody, err := gitproxy.DownloadToFile(dlClient, req, tmpPath, releaseDownloadTimeout)
	if err != nil {
		_ = os.Remove(tmpPath)
		if _, ok := gitproxy.AsLoginRedirect(err); ok {
			return "", formatReleaseAuthError(r.host, r.owner, r.repo, 0, err, r.hostClient)
		}
		return "", fmt.Errorf("downloading %s from %s/%s/%s release %s: %w", asset.Name, r.host, r.owner, r.repo, r.tag, err)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		_ = os.Remove(tmpPath)
		return "", formatReleaseAuthError(r.host, r.owner, r.repo, status, nil, r.hostClient)
	}
	if status != http.StatusOK {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("downloading %s from %s/%s/%s release %s: HTTP %d: %s",
			asset.Name, r.host, r.owner, r.repo, r.tag, status, truncate(errBody, 200))
	}
	if !strings.EqualFold(gotSHA, expectedSHA) {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("%w: sha256 mismatch for %s from %s/%s/%s release %s: got %s, expected %s",
			errReleaseIntegrity, asset.Name, r.host, r.owner, r.repo, r.tag, gotSHA, expectedSHA)
	}
	return tmpPath, nil
}

func (u *Upgrader) loadImageTarball(ctx context.Context, p *progress, step, tarPath string) (loadedRef, loadedID string, err error) {
	argv := u.cfg.Image.LoadCmd
	if len(argv) == 0 {
		argv = []string{"docker", "load", "-i"}
	}
	argv = append(append([]string{}, argv...), tarPath)
	out, err := runAsked(ctx, p, step, dockerLoadTimeout, argv)
	if err != nil {
		return "", "", fmt.Errorf("docker load %s: %w", filepath.Base(tarPath), err)
	}
	return ParseDockerLoadOutput(out)
}

func (u *Upgrader) inspectImageID(ctx context.Context, p *progress, step, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", errors.New("empty image reference")
	}
	argv := u.cfg.Image.InspectIDCmd
	if len(argv) == 0 {
		argv = []string{"docker", "image", "inspect", "--format", "{{.Id}}"}
	}
	argv = append(append([]string{}, argv...), ref)
	out, err := runAsked(ctx, p, step, imageInspectTimeout, argv)
	if err != nil {
		return "", err
	}
	id := strings.ToLower(strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]))
	if !strings.HasPrefix(id, "sha256:") && hex64Pattern.MatchString(id) {
		id = "sha256:" + id
	}
	if !imageIDPattern.MatchString(id) {
		return "", fmt.Errorf("docker image inspect returned invalid ID %q for %s", id, ref)
	}
	return id, nil
}

func (u *Upgrader) releaseMetaPath() string {
	if u.cfg.Image == nil || u.cfg.Image.RefFile == "" {
		return ""
	}
	return u.cfg.Image.RefFile + ".release.json"
}

func (u *Upgrader) readReleaseMeta() (recordedReleaseImage, bool) {
	path := u.releaseMetaPath()
	if path == "" {
		return recordedReleaseImage{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return recordedReleaseImage{}, false
	}
	var m recordedReleaseImage
	if err := json.Unmarshal(raw, &m); err != nil {
		return recordedReleaseImage{}, false
	}
	return m, true
}

func (u *Upgrader) writeReleaseMeta(m *recordedReleaseImage) error {
	path := u.releaseMetaPath()
	if path == "" {
		return nil
	}
	if m == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (u *Upgrader) isReleaseDaemonUnchanged(ctx context.Context, currentRef, targetTag string, r *resolvedRelease, expectedSHA, expectedImageID string) bool {
	currentRef = strings.TrimSpace(currentRef)
	if currentRef == "" {
		return false
	}
	if !sameRepository(repositoryOf(currentRef), repositoryOf(targetTag)) {
		return false
	}
	// 1. Compare local Docker image ID of currentRef against manifest's imageId.
	if expectedImageID != "" {
		if curID, err := u.inspectImageID(ctx, nil, "", currentRef); err == nil && sameImageID(curID, expectedImageID) {
			return true
		}
	}
	// 2. Compare recorded release metadata sidecar from the previous upgrade.
	if meta, ok := u.readReleaseMeta(); ok && meta.Ref == currentRef {
		if expectedImageID != "" && meta.ImageID != "" && sameImageID(meta.ImageID, expectedImageID) {
			return true
		}
		if expectedSHA != "" && meta.AssetSHA != "" && strings.EqualFold(meta.AssetSHA, expectedSHA) {
			return true
		}
		if r.manifest != nil && r.manifest.Commit != "" && meta.Commit != "" && strings.EqualFold(meta.Commit, r.manifest.Commit) {
			return true
		}
	}
	// 3. Compare running build commit against manifest commit (or 40-hex release target_commitish).
	if rev := u.runningCommit(); rev != "" {
		if r.manifest != nil && r.manifest.Commit != "" && strings.EqualFold(rev, r.manifest.Commit) {
			return true
		}
		if r.manifest == nil && hex40Pattern.MatchString(strings.ToLower(strings.TrimSpace(r.release.TargetCommitish))) &&
			strings.EqualFold(rev, strings.TrimSpace(r.release.TargetCommitish)) {
			return true
		}
	}
	return false
}

func (u *Upgrader) runningCommit() string {
	if u.cfg.RunningCommit != nil {
		return strings.TrimSpace(u.cfg.RunningCommit())
	}
	return ""
}

// alreadyRunningReleaseAssets checks whether the target branch's release on
// GitHub/GHES carries the exact daemon image already running, fetching only
// release JSON and images.json / .sha256 sidecar (never downloading a tarball).
func (u *Upgrader) alreadyRunningReleaseAssets(ctx context.Context, branch, currentRef, targetTag string) (bool, error) {
	cfg := u.EffectiveReleaseAssets()
	r, err := u.resolveRelease(ctx, branch)
	if err != nil {
		return false, err
	}
	_, expectedSHA, expectedImageID, _, err := r.expectedAssetSpec(cfg, ReleaseRoleGrain, cfg.Asset, cfg.ChecksumAsset)
	if err != nil {
		return false, err
	}
	return u.isReleaseDaemonUnchanged(ctx, currentRef, targetTag, r, expectedSHA, expectedImageID), nil
}

// stageReleaseDaemon resolves the release for branch, checks unchanged before
// downloading, and if changed downloads, verifies sha256, runs `docker load`,
// and verifies the loaded image ID. It deliberately does NOT move targetTag yet
// so cutover only happens at stepRecord after all checks pass.
func (u *Upgrader) stageReleaseDaemon(ctx context.Context, p *progress, branch, prevRef, targetTag string) (*stagedReleaseDaemon, bool, error) {
	cfg := u.EffectiveReleaseAssets()
	r, err := u.resolveRelease(ctx, branch)
	if err != nil {
		return nil, false, err
	}
	tarball, expectedSHA, expectedImageID, expectedRef, err := r.expectedAssetSpec(cfg, ReleaseRoleGrain, cfg.Asset, cfg.ChecksumAsset)
	if err != nil {
		return nil, false, err
	}

	if u.isReleaseDaemonUnchanged(ctx, prevRef, targetTag, r, expectedSHA, expectedImageID) {
		return &stagedReleaseDaemon{
			resolved:  r,
			targetTag: targetTag,
			imageID:   expectedImageID,
			assetName: tarball.Name,
			assetSHA:  expectedSHA,
		}, true, nil
	}

	// If local Docker already holds expectedRef at expectedImageID, skip
	// re-downloading the tarball.
	if expectedRef != "" && expectedImageID != "" {
		if localID, err := u.inspectImageID(ctx, p, stepPull, expectedRef); err == nil && sameImageID(localID, expectedImageID) {
			commit := ""
			if r.manifest != nil {
				commit = r.manifest.Commit
			}
			return &stagedReleaseDaemon{
				resolved:  r,
				stagedRef: expectedRef,
				targetTag: targetTag,
				loadedRef: expectedRef,
				imageID:   localID,
				assetName: tarball.Name,
				assetSHA:  expectedSHA,
				commit:    commit,
			}, false, nil
		}
	}

	// Every tarball the release names is checked before the first one is
	// loaded, so a missing or mismatched sandbox asset is refused here
	// rather than after the daemon image is already in Docker.
	if err := r.verifyManifestImages(cfg); err != nil {
		return nil, false, err
	}
	tmpPath, err := u.streamAndVerifyAsset(ctx, p, stepPull, r, cfg, tarball, expectedSHA)
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(tmpPath)

	loadedRef, loadedID, err := u.loadImageTarball(ctx, p, stepPull, tmpPath)
	if err != nil {
		return nil, false, err
	}

	inspectCandidate := loadedRef
	if inspectCandidate == "" {
		inspectCandidate = expectedRef
	}
	if inspectCandidate == "" {
		inspectCandidate = loadedID
	}
	actualID, err := u.inspectImageID(ctx, p, stepPull, inspectCandidate)
	if err != nil {
		return nil, false, fmt.Errorf("inspecting loaded daemon image %s: %w", inspectCandidate, err)
	}
	if loadedID != "" && !sameImageID(actualID, loadedID) {
		return nil, false, fmt.Errorf("%w: loaded daemon image ID mismatch: `docker load` reported %s but inspect found %s", errReleaseIntegrity, loadedID, actualID)
	}
	if expectedImageID != "" && !sameImageID(actualID, expectedImageID) {
		return nil, false, fmt.Errorf("%w: loaded daemon image %s has ID %s, but %s expects %s",
			errReleaseIntegrity, tarball.Name, actualID, cfg.ManifestAsset, expectedImageID)
	}

	stagedRef := loadedRef
	if expectedRef != "" && expectedRef != loadedRef {
		if err := u.tagImage(ctx, p, stepPull, actualID, expectedRef); err != nil {
			return nil, false, fmt.Errorf("tagging loaded daemon image %s as %s: %w", actualID, expectedRef, err)
		}
		stagedRef = expectedRef
	}
	if stagedRef == "" {
		stagedRef = repositoryOf(targetTag) + ":staged-" + strings.TrimPrefix(actualID, "sha256:")[:12]
		if err := u.tagImage(ctx, p, stepPull, actualID, stagedRef); err != nil {
			return nil, false, fmt.Errorf("tagging loaded daemon image %s as %s: %w", actualID, stagedRef, err)
		}
	}
	if expectedRef != "" {
		if refID, err := u.inspectImageID(ctx, p, stepPull, expectedRef); err != nil || !sameImageID(refID, expectedImageID) {
			return nil, false, fmt.Errorf("loaded daemon image ref %s did not resolve to expected ID %s", expectedRef, expectedImageID)
		}
	}

	commit := ""
	if r.manifest != nil {
		commit = r.manifest.Commit
	}
	return &stagedReleaseDaemon{
		resolved:  r,
		stagedRef: stagedRef,
		targetTag: targetTag,
		loadedRef: loadedRef,
		imageID:   actualID,
		assetName: tarball.Name,
		assetSHA:  expectedSHA,
		commit:    commit,
	}, false, nil
}

// verifyManifestImages checks that every sandbox image r's manifest lists
// (guest, k8s-sandbox) has its tarball and a .sha256 sidecar on the
// release that holds it, agreeing with the manifest's sha256. The daemon
// image's own spec is checked by its caller; the tarball bytes are hashed
// as they stream, before `docker load` sees them.
func (r *resolvedRelease) verifyManifestImages(cfg ReleaseAssetsConfig) error {
	if r == nil || r.manifest == nil {
		return nil
	}
	for _, role := range []string{ReleaseRoleGuest, ReleaseRoleK8sSandbox} {
		if _, ok := r.manifest.Images[role]; !ok {
			continue
		}
		if _, _, _, _, err := r.expectedAssetSpec(cfg, role, "", ""); err != nil {
			return fmt.Errorf("%s image: %w", role, err)
		}
	}
	return nil
}

// stageReleaseSandbox loads the candidate daemon's expected sandbox image from
// the same release's images.json manifest or configured SandboxAssets. If the
// sandbox image is not published as a release asset, it tries pulling from its
// container registry and fails clearly before cutover (naming the sandbox
// image) when unreachable.
func (u *Upgrader) stageReleaseSandbox(ctx context.Context, p *progress, r *resolvedRelease, sandboxRef, candidateRef string) (fromAssets bool, err error) {
	cfg := u.EffectiveReleaseAssets()
	var role string
	var explicitAsset string

	// 1. Match against images.json roles by exact Ref or by SandboxImageArgs role.
	if r != nil && r.manifest != nil {
		for k, entry := range r.manifest.Images {
			if k == ReleaseRoleGrain {
				continue
			}
			if entry.Ref == sandboxRef {
				role = k
				break
			}
		}
		if role == "" {
			inferred := inferSandboxRole(u.cfg.Image.SandboxImageArgs, sandboxRef)
			if entry, ok := r.manifest.Images[inferred]; ok && cfg.SandboxAssets[inferred] != "" {
				role = inferred
				_ = entry
			}
		}
	}

	// 2. Match against explicit SandboxAssets configuration (by role, full ref, or repository).
	if role == "" {
		inferred := inferSandboxRole(u.cfg.Image.SandboxImageArgs, sandboxRef)
		for _, key := range []string{sandboxRef, repositoryOf(sandboxRef), inferred} {
			if key != "" && strings.TrimSpace(cfg.SandboxAssets[key]) != "" {
				role = inferred
				explicitAsset = strings.TrimSpace(cfg.SandboxAssets[key])
				break
			}
		}
	}

	if role == "" && explicitAsset == "" {
		// Not in release assets: try registry pull, and if that fails, report a
		// clear pre-cutover error naming the sandbox image so we never silently
		// keep the old sandbox image.
		if pullErr := u.pullImage(ctx, p, stepSandbox, sandboxRef); pullErr != nil {
			hint := registryHint(sandboxRef, pullErr)
			if hint != "" {
				hint = " (" + hint + ")"
			}
			return false, fmt.Errorf("candidate image %s expects sandbox image %s, which is not in release %s on %s/%s/%s and could not be pulled from its registry%s: %w",
				candidateRef, sandboxRef, r.tag, r.host, r.owner, r.repo, hint, pullErr)
		}
		return false, nil
	}

	tarball, expectedSHA, expectedImageID, expectedRef, err := r.expectedAssetSpec(cfg, role, explicitAsset, "")
	if err != nil {
		return true, fmt.Errorf("sandbox image %s: %w", sandboxRef, err)
	}

	// If sandboxRef is already present locally with the exact expected imageId,
	// reuse it without downloading.
	if expectedImageID != "" {
		if localID, inspectErr := u.inspectImageID(ctx, p, stepSandbox, sandboxRef); inspectErr == nil && sameImageID(localID, expectedImageID) {
			return true, nil
		}
	}

	tmpPath, err := u.streamAndVerifyAsset(ctx, p, stepSandbox, r, cfg, tarball, expectedSHA)
	if err != nil {
		return true, fmt.Errorf("sandbox image %s: %w", sandboxRef, err)
	}
	defer os.Remove(tmpPath)

	loadedRef, loadedID, err := u.loadImageTarball(ctx, p, stepSandbox, tmpPath)
	if err != nil {
		return true, fmt.Errorf("sandbox image %s: %w", sandboxRef, err)
	}

	inspectCandidate := loadedRef
	if inspectCandidate == "" {
		inspectCandidate = expectedRef
	}
	if inspectCandidate == "" {
		inspectCandidate = loadedID
	}
	actualID, err := u.inspectImageID(ctx, p, stepSandbox, inspectCandidate)
	if err != nil {
		return true, fmt.Errorf("inspecting loaded sandbox image %s: %w", sandboxRef, err)
	}
	if expectedImageID != "" && !sameImageID(actualID, expectedImageID) {
		return true, fmt.Errorf("loaded sandbox image %s (%s) has ID %s, but %s expects %s",
			sandboxRef, tarball.Name, actualID, cfg.ManifestAsset, expectedImageID)
	}
	if sandboxRef != loadedRef {
		if err := u.tagImage(ctx, p, stepSandbox, actualID, sandboxRef); err != nil {
			return true, fmt.Errorf("tagging loaded sandbox image %s as %s: %w", actualID, sandboxRef, err)
		}
	}
	if refID, err := u.inspectImageID(ctx, p, stepSandbox, sandboxRef); err != nil || (expectedImageID != "" && !sameImageID(refID, expectedImageID)) {
		return true, fmt.Errorf("sandbox image ref %s did not resolve to expected ID %s", sandboxRef, expectedImageID)
	}
	return true, nil
}

func inferSandboxRole(args []string, sandboxRef string) string {
	for _, a := range args {
		if strings.Contains(a, "k8s-sandbox") || strings.Contains(a, "sandbox-k8s") {
			return ReleaseRoleK8sSandbox
		}
	}
	repo := repositoryOf(sandboxRef)
	if strings.Contains(repo, "k8s-sandbox") || strings.Contains(repo, "sandbox-k8s") {
		return ReleaseRoleK8sSandbox
	}
	return ReleaseRoleGuest
}

// listReleaseTags fetches release tags from the configured GitHub / GitHub
// Enterprise repository (`GET /repos/{owner}/{repo}/releases?per_page=100`),
// translating `channel-<tag>` moving tags back to `<tag>` so Targets,
// LatestRelease, DiscardedRC and RetractedRelease work identically to registry
// tag listings.
func (u *Upgrader) listReleaseTags(ctx context.Context) ([]string, error) {
	cfg := u.EffectiveReleaseAssets()
	if strings.TrimSpace(cfg.Repository) == "" {
		return nil, errors.New("no release repository configured (-upgrade-release-repo)")
	}
	host, owner, repo, apiBase, hc, err := u.resolveReleaseClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	res := &resolvedRelease{
		host:       host,
		owner:      owner,
		repo:       repo,
		apiBase:    apiBase,
		hostClient: hc,
	}
	releases, err := res.listReleases(ctx, nil)
	if err != nil {
		return nil, err
	}
	// A deployment that loads release assets and nothing else is never
	// offered a version (or candidate) its publisher tagged without
	// publishing: Targets, LatestRelease and the automatic upgrade all
	// read this list.
	publishedOnly := u.releaseAssetsOnly()
	requiredAsset := requiredReleaseAsset(cfg)
	var tags []string
	seen := map[string]bool{}
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		tag := strings.TrimSpace(rel.TagName)
		if publishedOnly && (versionTag.MatchString(tag) || candidateTag.MatchString(tag)) && countAssets(rel, requiredAsset) == 0 {
			continue
		}
		if ch, ok := strings.CutPrefix(tag, "channel-"); ok && ch != "" {
			add(ch)
		}
		add(tag)
	}
	return tags, nil
}
