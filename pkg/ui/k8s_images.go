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

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registryauth"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
	"github.com/ai-on-gke/grain/pkg/upgrade"
	"github.com/ai-on-gke/grain/pkg/version"
)

// ErrImageNotFound is returned by CheckImageManifest when a container registry
// definitively reports HTTP 404 Not Found for the requested image manifest.
var ErrImageNotFound = errors.New("image manifest not found")

// ImageTagForVersion derives the default container image tag corresponding to
// the running daemon's build version (pkg/version).
//
//   - Release builds (e.g. "v2.2.0") return the release tag ("v2.2.0").
//   - Main and candidate builds stamped by scripts/build-version.sh
//     (e.g. "v2.3.0-dev.114+8e1a209" or "v2.2.0-rc.1.48+6f65669") return the
//     immutable commit tag ("sha-8e1a209") published by scripts/release-images.sh.
//   - Dev builds (unstamped local builds, test binaries, or dirty -dev trees)
//     fall back to "latest".
func ImageTagForVersion(info version.Info) string {
	rel := strings.TrimSpace(info.Release)
	rev := strings.TrimSpace(info.Revision)
	if info.Unversioned || (rel == "" && rev == "") || rel == "(devel)" || rel == "dev" {
		return "latest"
	}
	if info.Modified && (rel == "" || strings.Contains(rel, "-dev.")) {
		return "latest"
	}
	if _, meta, ok := strings.Cut(rel, "+"); ok && strings.TrimSpace(meta) != "" {
		short := strings.TrimSpace(meta)
		if strings.HasPrefix(short, "sha-") {
			return short
		}
		return "sha-" + shortRevision(short)
	}
	if strings.HasPrefix(rel, "sha-") {
		return rel
	}
	if strings.Contains(rel, "-dev.") || rel == "main" || rel == "candidate" || rel == "" {
		if rev != "" {
			return "sha-" + shortRevision(rev)
		}
		return "latest"
	}
	return rel
}

// DaemonBuildVersion returns the human-readable daemon build version string
// shown in the Kubernetes image setting help text ("matches this daemon's build <version>").
func DaemonBuildVersion(info version.Info) string {
	rel := strings.TrimSpace(info.Release)
	rev := strings.TrimSpace(info.Revision)
	if info.Unversioned || (rel == "" && rev == "") || rel == "(devel)" || rel == "dev" {
		return "dev"
	}
	if info.Modified && (rel == "" || strings.Contains(rel, "-dev.")) {
		return "dev"
	}
	if rel != "" {
		return rel
	}
	if rev != "" {
		return "sha-" + shortRevision(rev)
	}
	return "dev"
}

func shortRevision(rev string) string {
	rev = strings.TrimSpace(rev)
	if len(rev) > 7 && isHexString(rev) {
		return rev[:7]
	}
	return rev
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// ImageRepositoryPrefix returns the container registry/repository prefix
// (e.g. "docker.io/grainai") for the given install track, derived
// from installtrack.ImageRepository, which honours the track's image
// repository override (GRAIN_OSS_IMAGE_REPO for core's).
func ImageRepositoryPrefix(track installtrack.Track) string {
	return repositoryPrefixFromDaemonRepo(installtrack.ImageRepository(track))
}

func repositoryPrefixFromDaemonRepo(daemonRepo string) string {
	repo := strings.TrimSpace(daemonRepo)
	if at := strings.IndexByte(repo, '@'); at >= 0 {
		repo = repo[:at]
	}
	if slash := strings.LastIndexByte(repo, '/'); slash >= 0 {
		if colon := strings.IndexByte(repo[slash+1:], ':'); colon >= 0 {
			repo = repo[:slash+1+colon]
		}
	}
	repo = strings.TrimSuffix(repo, "/")
	if slash := strings.LastIndexByte(repo, '/'); slash > 0 {
		return repo[:slash]
	}
	if repo == "" {
		return "docker.io/grainai"
	}
	return repo
}

func isLegacyGHCRPrefix(prefix string) bool {
	return prefix == "ghcr.io/bwsalmon/grain" || prefix == "ghcr.io/ai-on-gke/grain"
}

// DefaultK8sSandboxImageFor returns the build-pinned, track-aware default
// sandbox-k8s image reference for track and info.
func DefaultK8sSandboxImageFor(track installtrack.Track, info version.Info) string {
	prefix := ImageRepositoryPrefix(track)
	if isLegacyGHCRPrefix(prefix) {
		return prefix + "/k8s-sandbox:" + ImageTagForVersion(info)
	}
	return prefix + "/sandbox-k8s:" + ImageTagForVersion(info)
}

// DefaultSandboxImageFor returns the build-pinned, track-aware default
// sandbox (Granule VM guest) image reference for track and info.
func DefaultSandboxImageFor(track installtrack.Track, info version.Info) string {
	prefix := ImageRepositoryPrefix(track)
	if isLegacyGHCRPrefix(prefix) {
		return prefix + "/guest:" + ImageTagForVersion(info)
	}
	return prefix + "/sandbox:" + ImageTagForVersion(info)
}

// ResolveDefaultK8sSandboxImage returns the default k8s-sandbox image for track
// and info. When tagExists is provided and reports that the candidate tag does
// not exist in the track's primary registry, it falls back to the OSS registry
// so a deployment is never pointed at an unpopulated registry.
func ResolveDefaultK8sSandboxImage(track installtrack.Track, info version.Info, tagExists func(string) bool) string {
	candidate := DefaultK8sSandboxImageFor(track, info)
	ossFallback := DefaultK8sSandboxImageFor(installtrack.TrackOSS, info)
	if candidate != ossFallback && tagExists != nil && !tagExists(candidate) {
		return ossFallback
	}
	return candidate
}

// ResolveDefaultSandboxImage returns the default guest (Granule VM) image for
// track and info. When tagExists is provided and reports that the candidate tag
// does not exist in the track's primary registry, it falls back to the OSS
// registry so a deployment is never pointed at an unpopulated registry.
func ResolveDefaultSandboxImage(track installtrack.Track, info version.Info, tagExists func(string) bool) string {
	candidate := DefaultSandboxImageFor(track, info)
	ossFallback := DefaultSandboxImageFor(installtrack.TrackOSS, info)
	if candidate != ossFallback && tagExists != nil && !tagExists(candidate) {
		return ossFallback
	}
	return candidate
}

type cachedManifestResult struct {
	err       error
	expiresAt time.Time
}

type manifestCheckFlight struct {
	done chan struct{}
	err  error
}

var (
	manifestCheckCache     sync.Map // map[string]cachedManifestResult
	manifestCheckFlightsMu sync.Mutex
	manifestCheckFlights   = map[string]*manifestCheckFlight{}
)

const (
	manifestCheckTTL     = 5 * time.Minute
	registryTokenTimeout = 1 * time.Second
)

// CheckImageManifest performs a cheap OCI/Docker Registry v2 manifest HEAD
// check for imageRef. It supports anonymous Bearer token exchange (e.g. GHCR),
// sends this host's Google access token to Artifact Registry and GCR
// (registryauth.TokenFor), caches results in memory for 5 minutes, and
// coalesces concurrent checks for the same image reference.
func CheckImageManifest(ctx context.Context, imageRef string) error {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return nil
	}
	now := time.Now()
	if cached, ok := manifestCheckCache.Load(imageRef); ok {
		entry := cached.(cachedManifestResult)
		if now.Before(entry.expiresAt) {
			return entry.err
		}
	}
	manifestCheckFlightsMu.Lock()
	if cached, ok := manifestCheckCache.Load(imageRef); ok {
		entry := cached.(cachedManifestResult)
		if time.Now().Before(entry.expiresAt) {
			manifestCheckFlightsMu.Unlock()
			return entry.err
		}
	}
	if flight, ok := manifestCheckFlights[imageRef]; ok {
		manifestCheckFlightsMu.Unlock()
		select {
		case <-flight.done:
			return flight.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		manifestCheckFlightsMu.Unlock()
		return err
	}
	flight := &manifestCheckFlight{done: make(chan struct{})}
	manifestCheckFlights[imageRef] = flight
	manifestCheckFlightsMu.Unlock()

	err := errors.New("image manifest check aborted")
	defer func() {
		if !errors.Is(err, context.Canceled) {
			manifestCheckCache.Store(imageRef, cachedManifestResult{
				err:       err,
				expiresAt: time.Now().Add(manifestCheckTTL),
			})
		}
		manifestCheckFlightsMu.Lock()
		flight.err = err
		close(flight.done)
		delete(manifestCheckFlights, imageRef)
		manifestCheckFlightsMu.Unlock()
	}()

	err = checkImageManifestUncached(context.WithoutCancel(ctx), imageRef)
	return err
}

func checkImageManifestUncached(ctx context.Context, imageRef string) error {
	host, repo, ref, err := parseOCIImageReference(imageRef)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tokCtx, tokCancel := context.WithTimeout(ctx, registryTokenTimeout)
	googleToken := registryauth.TokenFor(tokCtx, host)
	tokCancel()

	client := &http.Client{Timeout: 2 * time.Second, Transport: registryTransport}
	scheme := "https"
	if isLoopbackRegistryHost(host) {
		scheme = "http"
	}
	manifestURL := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, url.PathEscape(ref))

	doHead := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", strings.Join([]string{
			"application/vnd.oci.image.index.v1+json",
			"application/vnd.oci.image.manifest.v1+json",
			"application/vnd.docker.distribution.manifest.list.v2+json",
			"application/vnd.docker.distribution.manifest.v2+json",
		}, ", "))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}

	resp, err := doHead(googleToken)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		if chal := resp.Header.Get("WWW-Authenticate"); chal != "" {
			if tok, tokErr := fetchRegistryBearerToken(ctx, client, chal); tokErr == nil && tok != "" {
				resp, err = doHead(tok)
				if err != nil {
					return err
				}
				resp.Body.Close()
			}
		}
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s does not resolve in %s (HTTP 404)", ErrImageNotFound, imageRef, host)
	default:
		return fmt.Errorf("registry %s returned HTTP %d for %s", host, resp.StatusCode, imageRef)
	}
}

func parseOCIImageReference(imageRef string) (host, repo, ref string, err error) {
	s := strings.TrimSpace(imageRef)
	if s == "" {
		return "", "", "", errors.New("empty image reference")
	}
	ref = "latest"
	if at := strings.IndexByte(s, '@'); at >= 0 {
		ref = s[at+1:]
		s = s[:at]
		if slash := strings.LastIndexByte(s, '/'); slash >= 0 {
			if colon := strings.IndexByte(s[slash+1:], ':'); colon >= 0 {
				s = s[:slash+1+colon]
			}
		}
	} else if slash := strings.LastIndexByte(s, '/'); slash >= 0 {
		if colon := strings.IndexByte(s[slash+1:], ':'); colon >= 0 {
			ref = s[slash+1+colon+1:]
			s = s[:slash+1+colon]
		}
	} else if colon := strings.IndexByte(s, ':'); colon >= 0 {
		ref = s[colon+1:]
		s = s[:colon]
	}
	slash := strings.IndexByte(s, '/')
	if slash <= 0 || slash == len(s)-1 {
		return "", "", "", fmt.Errorf("invalid image reference %q", imageRef)
	}
	first := s[:slash]
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		host = "registry-1.docker.io"
		repo = s
	} else {
		host = first
		repo = s[slash+1:]
		if host == "docker.io" || host == "index.docker.io" {
			host = "registry-1.docker.io"
			if !strings.Contains(repo, "/") {
				repo = "library/" + repo
			}
		}
	}
	if ref == "" {
		ref = "latest"
	}
	return host, repo, ref, nil
}

func isLoopbackRegistryHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "localhost" ||
		strings.HasPrefix(h, "localhost:") ||
		h == "127.0.0.1" ||
		strings.HasPrefix(h, "127.") ||
		strings.HasPrefix(h, "[::1]")
}

func fetchRegistryBearerToken(ctx context.Context, client *http.Client, challenge string) (string, error) {
	rest := strings.TrimSpace(challenge)
	if len(rest) < 7 || !strings.EqualFold(rest[:7], "Bearer ") {
		return "", errors.New("not a Bearer challenge")
	}
	params := parseBearerParams(rest[7:])
	realm := params["realm"]
	if realm == "" {
		return "", errors.New("missing Bearer realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if sc := params["scope"]; sc != "" {
		q.Set("scope", sc)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	return body.AccessToken, nil
}

func parseBearerParams(s string) map[string]string {
	out := make(map[string]string)
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t,")
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = s[eq+1:]
		var val string
		if len(s) > 0 && s[0] == '"' {
			s = s[1:]
			var b strings.Builder
			for len(s) > 0 {
				ch := s[0]
				s = s[1:]
				if ch == '\\' && len(s) > 0 {
					b.WriteByte(s[0])
					s = s[1:]
					continue
				}
				if ch == '"' {
					break
				}
				b.WriteByte(ch)
			}
			val = b.String()
		} else {
			comma := strings.IndexByte(s, ',')
			if comma < 0 {
				val = strings.TrimSpace(s)
				s = ""
			} else {
				val = strings.TrimSpace(s[:comma])
				s = s[comma+1:]
			}
		}
		out[key] = val
	}
	return out
}

func (c *Client) daemonVersionInfo() version.Info {
	if c != nil && c.Config.DaemonVersion != nil {
		return c.Config.DaemonVersion()
	}
	return version.Get()
}

func (c *Client) detectInstallTrack(cfg *model.Config, repoConfigs []model.RepoConfig) installtrack.Track {
	var probe installtrack.Probe
	if c != nil && c.Config.InstallTrackProbe != nil {
		probe = *c.Config.InstallTrackProbe
	}
	probe.Config = cfg
	probe.RepoConfigs = repoConfigs
	if probe.DataDir == "" && c != nil {
		probe.DataDir = c.Config.DataDir
	}
	return probe.Detect().Track
}

func (c *Client) checkImageManifest(ctx context.Context, imageRef string) error {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return nil
	}
	if c != nil && c.Config.CheckImageManifest != nil {
		return c.Config.CheckImageManifest(ctx, imageRef)
	}
	host, _, _, err := parseOCIImageReference(imageRef)
	if err == nil && isLoopbackRegistryHost(host) {
		return CheckImageManifest(ctx, imageRef)
	}
	return nil
}

func (c *Client) resolveK8sImageDefaults(ctx context.Context, cfg *model.Config, repoConfigs []model.RepoConfig) (sandboxDefault, granuleDefault, buildVersion string) {
	info := c.daemonVersionInfo()
	track := c.detectInstallTrack(cfg, repoConfigs)
	var tagExists func(string) bool
	if c != nil && c.Config.CheckImageManifest != nil {
		tagExists = func(ref string) bool {
			return c.Config.CheckImageManifest(ctx, ref) == nil
		}
	}
	sandboxDefault = ResolveDefaultK8sSandboxImage(track, info, tagExists)
	granuleDefault = ResolveDefaultSandboxImage(track, info, tagExists)
	buildVersion = DaemonBuildVersion(info)
	return sandboxDefault, granuleDefault, buildVersion
}

// k8sGranuleImageForWarn is the Granule VM pod image the Settings pane
// probes and reports as K8sGranuleImageEffective: the one the daemon
// really creates pods from (Config.K8sGranuleImage), and only where no
// daemon says so, cfg's own image -- unless mirror reads it as the
// mirror rather than an image -- or granuleDefault, mirrored into
// mirror's repository.
func (c *Client) k8sGranuleImageForWarn(cfg model.Config, granuleDefault string, mirror SandboxImageMirrorInfo) string {
	if c != nil && c.Config.K8sGranuleImage != nil {
		if img := c.Config.K8sGranuleImage(cfg); img != "" {
			return img
		}
	}
	img := firstNonEmptyString(cfg.K8sGranuleImage, granuleDefault)
	if mirror.K8sGranuleImageIsMirror {
		img = granuleDefault
	}
	if repo := strings.TrimSpace(mirror.Repository); repo != "" {
		if mirrored := upgrade.MirrorSandboxRef(img, repo); mirrored != "" {
			img = mirrored
		}
	}
	return img
}

// SandboxImageMirrorInfo is Config.SandboxImageMirrorFor's answer: the
// repository the stamped guest is mirrored into under a configuration,
// where it comes from, and which image settings it overrides.
type SandboxImageMirrorInfo struct {
	// Repository is the mirror repository, without tag or digest; ""
	// mirrors nothing.
	Repository string
	// DerivedFrom names the setting Repository was derived from
	// (sandboximage.MirrorHint.Field) when sandbox_image_mirror is empty;
	// "" when it is that setting, or there is no mirror.
	DerivedFrom string
	// KubernetesGranule is whether the configuration runs the
	// kubernetes-granule backend, the only one a mirror is derived on.
	KubernetesGranule bool
	// K8sGranuleImageIsMirror and SandboxImageIsMirror are whether
	// k8s_granule_image and sandbox_image are read as Repository (a
	// mirrored guest ref in it) rather than booted as images.
	K8sGranuleImageIsMirror bool
	SandboxImageIsMirror    bool
}

// sandboxImageMirrorFor is Config.SandboxImageMirrorFor, or where no
// daemon answers it, sandboxImageMirrorFromConfig against
// granuleDefault's repository as the stamped one.
func (c *Client) sandboxImageMirrorFor(cfg model.Config, granuleDefault string) SandboxImageMirrorInfo {
	if c != nil && c.Config.SandboxImageMirrorFor != nil {
		return c.Config.SandboxImageMirrorFor(cfg)
	}
	return sandboxImageMirrorFromConfig(cfg, granuleDefault)
}

// sandboxImageMirrorFromConfig resolves the mirror from the stored row
// alone, by the rule the daemon applies (sandboximage.DerivedMirror) --
// all of it but the -sandbox-image flag, which no row carries.
func sandboxImageMirrorFromConfig(cfg model.Config, granuleDefault string) SandboxImageMirrorInfo {
	info := SandboxImageMirrorInfo{KubernetesGranule: cfg.SandboxBackend == model.SandboxBackendKubernetesGranule}
	if explicit := strings.TrimSpace(cfg.SandboxImageMirror); explicit != "" {
		info.Repository = sandboximage.ImageRepository(explicit)
	} else if info.KubernetesGranule {
		if hint, ok := sandboximage.DerivedMirror(cfg.K8sGranuleImage, "", sandboximage.ImageRepository(granuleDefault)); ok {
			info.Repository, info.DerivedFrom = hint.Repository, hint.Field
		}
	}
	if info.Repository != "" && info.KubernetesGranule {
		stampedTag := sandboximage.ImageTag(granuleDefault)
		info.K8sGranuleImageIsMirror = sandboximage.IsMirroredGuestRef(cfg.K8sGranuleImage, info.Repository, stampedTag)
		info.SandboxImageIsMirror = sandboximage.IsMirroredGuestRef(cfg.SandboxImage, info.Repository, stampedTag)
	}
	return info
}

// keepDerivedSandboxImageMirror is the save's guard on a mirror derived
// from k8s_granule_image: a pin that is a mirrored guest ref is ignored
// as an image and load-bearing as the mirror target, so clearing it --
// what anyone reading "k8s_granule_image: <old sha>" would do -- would
// silently stop mirroring at the next restart. When before derived a
// mirror that after, still on kubernetes-granule with no
// sandbox_image_mirror of its own, would not, the repository is written
// into sandbox_image_mirror in the same save, so the mirror becomes
// explicit instead of disappearing. It is said nowhere but in the row:
// a plan runs this too, and the save's own "changed ..." line names
// SandboxImageMirror.
func (c *Client) keepDerivedSandboxImageMirror(before, after model.Config) model.Config {
	if strings.TrimSpace(after.SandboxImageMirror) != "" {
		return after
	}
	was := c.sandboxImageMirrorFor(before, "")
	if was.DerivedFrom == "" || was.Repository == "" {
		return after
	}
	now := c.sandboxImageMirrorFor(after, "")
	if !now.KubernetesGranule || now.Repository == was.Repository {
		return after
	}
	after.SandboxImageMirror = was.Repository
	return after
}

func (c *Client) resolveK8sImageWarnings(ctx context.Context, effectiveSandbox, effectiveGranule string) (sandboxWarn, granuleWarn string) {
	if err := c.checkImageManifest(ctx, effectiveSandbox); err != nil {
		sandboxWarn = fmt.Sprintf("Sandbox pod image %q did not resolve: %v", effectiveSandbox, err)
	} else if c != nil && c.Config.CheckClusterImageConnectivity != nil {
		if err := c.Config.CheckClusterImageConnectivity(ctx, effectiveSandbox); err != nil {
			sandboxWarn = err.Error()
		}
	}
	if err := c.checkImageManifest(ctx, effectiveGranule); err != nil {
		granuleWarn = fmt.Sprintf("Granule VM pod image %q did not resolve: %v", effectiveGranule, err)
	} else if c != nil && c.Config.CheckClusterImageConnectivity != nil {
		if err := c.Config.CheckClusterImageConnectivity(ctx, effectiveGranule); err != nil {
			granuleWarn = err.Error()
		}
	}
	return sandboxWarn, granuleWarn
}

func computeSettingSources(cfg model.Config, s Settings) map[string]string {
	strSource := func(stored, def string) string {
		if strings.TrimSpace(stored) != "" && strings.TrimSpace(stored) != strings.TrimSpace(def) {
			return "custom"
		}
		return "default"
	}
	sources := map[string]string{
		"k8sNamespace":           strSource(cfg.K8sNamespace, s.K8sNamespaceDefault),
		"k8sSandboxImage":        strSource(cfg.K8sSandboxImage, s.K8sSandboxImageDefault),
		"k8sGranuleImage":        strSource(cfg.K8sGranuleImage, s.K8sGranuleImageDefault),
		"sandboxImage":           strSource(cfg.SandboxImage, ""),
		"sandboxImageMirror":     strSource(cfg.SandboxImageMirror, ""),
		"k8sWorkspace":           "default",
		"k8sDeployment":          strSource(cfg.K8sDeployment, s.K8sDeploymentDefault),
		"k8sContainer":           strSource(cfg.K8sContainer, s.K8sContainerDefault),
		"k8sKubectl":             strSource(cfg.K8sKubectl, s.K8sKubectlDefault),
		"sandboxCpus":            "default",
		"sandboxMemoryMb":        "default",
		"sandboxDiskGb":          "default",
		"sandboxMemoryReserveMb": "default",
		"sandboxDiskFloorMinMb":  "default",
	}
	if strings.TrimSpace(cfg.K8sWorkspace) != "" {
		sources["k8sWorkspace"] = "custom"
	}
	if cfg.SandboxCPUs > 0 {
		sources["sandboxCpus"] = "custom"
	}
	if cfg.SandboxMemoryMB > 0 {
		sources["sandboxMemoryMb"] = "custom"
	}
	if cfg.SandboxDiskGB > 0 {
		sources["sandboxDiskGb"] = "custom"
	}
	if cfg.SandboxMemoryReserveMB >= 0 {
		sources["sandboxMemoryReserveMb"] = "custom"
	}
	if cfg.SandboxDiskFloorMinMB > 0 && cfg.SandboxDiskFloorMinMB != s.SandboxDiskFloorMinMBDefault {
		sources["sandboxDiskFloorMinMb"] = "custom"
	}
	return sources
}
