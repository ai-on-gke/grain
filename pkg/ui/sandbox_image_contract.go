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
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registryauth"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
)

// sandboxImageFields are the Settings fields about the custom sandbox
// image (model.Config.SandboxImage): what it is, what it defaults to on
// the active backend, which minimal base image a custom one is built
// FROM, and the contract verdict on the one configured.
type sandboxImageFields struct {
	image, imageDefault, baseImage, backend, warning string
	check                                            *sandboximage.CustomImageCheck
}

// sandboxImageBackend is the backend the custom image applies to: the
// one chosen in Settings, or the one this process is running when
// nothing is chosen there, or granule when neither says.
func (c *Client) sandboxImageBackend(cfg model.Config) string {
	if b := strings.TrimSpace(string(cfg.SandboxBackend)); b != "" {
		return b
	}
	if b := strings.TrimSpace(c.runningSandboxBackend()); b != "" {
		return b
	}
	return string(model.SandboxBackendGranule)
}

func (c *Client) resolveSandboxImageFields(ctx context.Context, cfg model.Config, podImageDefault, guestImageDefault string) sandboxImageFields {
	backend := c.sandboxImageBackend(cfg)
	out := sandboxImageFields{image: strings.TrimSpace(cfg.SandboxImage), backend: backend}
	if sandboximage.IsGranuleBackend(backend) {
		out.imageDefault = firstNonEmptyString(guestImageDefault, model.DefaultSandboxImage)
	} else {
		out.imageDefault = firstNonEmptyString(podImageDefault, model.DefaultK8sSandboxImage)
	}
	out.baseImage = sandboximage.BaseImageForBackend(backend, out.imageDefault)
	if out.image == "" {
		return out
	}
	if err := sandboximage.ValidateReference(out.image); err != nil {
		// Only the state repository can store one: the daemon ignores it
		// and creates sandboxes from the stock image, and says so here.
		out.warning = fmt.Sprintf("Custom sandbox image %s is ignored (%v); new sandboxes are created from %s.", out.image, err, out.imageDefault)
		return out
	}
	labels, err := c.sandboxImageLabels(ctx, out.image)
	check := sandboximage.CheckCustomImage(out.image, backend, out.baseImage, out.imageDefault, labels, err)
	out.check, out.warning = &check, check.Warning
	return out
}

func (f sandboxImageFields) apply(s *Settings) {
	s.SandboxImage = f.image
	s.SandboxImageDefault = f.imageDefault
	s.SandboxBaseImage = f.baseImage
	s.SandboxImageBackend = f.backend
	s.SandboxContractVersion = sandboximage.Contract
	s.SandboxMinimumContractVersion = sandboximage.MinimumContract
	s.SandboxGranuleContractVersion = sandboximage.GranuleContract
	s.SandboxImageWarning = f.warning
	s.SandboxImageCheck = f.check
	info := sandboximage.GetContractInfo(model.DefaultSandboxImage, model.DefaultK8sSandboxImage)
	s.SandboxContract = &info
}

// staleSandboxImagePins is every sandbox image setting in cfg --
// sandbox_image, k8s_sandbox_image, k8s_granule_image -- that pins
// grain's own stock sandbox image at another build than the one this
// daemon is stamped for (sandboximage.StaleStockPin), for the System
// Health pane: Settings says the same beside each field, but the pane is
// where somebody looks when a sandbox behaves oddly weeks after the pin.
//
// It reads no registry: tags decide, and a custom image's labels are
// used only when Settings has already fetched them (imageLabelCache).
func (c *Client) staleSandboxImagePins(ctx context.Context) []string {
	if c == nil || c.Store == nil {
		return nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return nil
	}
	if strings.TrimSpace(cfg.SandboxImage) == "" && strings.TrimSpace(cfg.K8sSandboxImage) == "" && strings.TrimSpace(cfg.K8sGranuleImage) == "" {
		return nil
	}
	repoConfigs, _ := c.Store.ListRepoConfigs(ctx)
	sandboxDefault, granuleDefault, _ := c.resolveK8sImageDefaults(ctx, cfg, repoConfigs)
	var out []string
	add := func(field, image, stamped string, labels map[string]string) {
		if w := sandboximage.StaleStockPin(image, stamped, labels); w != "" {
			out = append(out, field+": "+w)
		}
	}
	if image := strings.TrimSpace(cfg.SandboxImage); image != "" {
		stamped := sandboxDefault
		if sandboximage.IsGranuleBackend(c.sandboxImageBackend(*cfg)) {
			stamped = granuleDefault
		}
		var labels map[string]string
		if cached, ok := imageLabelCache.Load(image); ok {
			labels = cached.(cachedImageLabels).labels
		}
		add("sandbox_image", image, stamped, labels)
	}
	add("k8s_sandbox_image", cfg.K8sSandboxImage, sandboxDefault, nil)
	add("k8s_granule_image", cfg.K8sGranuleImage, granuleDefault, nil)
	return out
}

// joinWarnings is the non-empty warnings among ws, in order, as one.
func joinWarnings(ws ...string) string {
	var kept []string
	for _, w := range ws {
		if w = strings.TrimSpace(w); w != "" {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// sandboxImageLabels reads a custom image's OCI labels through
// Config.InspectImageLabels, remembering each answer for a few minutes:
// the Settings pane is read far more often than an image changes, and a
// registry round trip per read would make it slow for nothing.
func (c *Client) sandboxImageLabels(ctx context.Context, image string) (map[string]string, error) {
	if c == nil || c.Config.InspectImageLabels == nil {
		return nil, errors.New("this grain has no way to inspect sandbox images configured")
	}
	now := time.Now()
	if cached, ok := imageLabelCache.Load(image); ok {
		entry := cached.(cachedImageLabels)
		if now.Before(entry.expiresAt) {
			return entry.labels, entry.err
		}
	}
	labels, err := c.Config.InspectImageLabels(ctx, image)
	ttl := imageLabelTTL
	if err != nil {
		// A failure is retried sooner: the usual one is an image not yet
		// pushed, or not yet pulled to this host, and the operator who
		// just fixed that is looking at this pane waiting for it.
		ttl = imageLabelTTL / 5
	}
	imageLabelCache.Store(image, cachedImageLabels{labels: labels, err: err, expiresAt: now.Add(ttl)})
	return labels, err
}

type cachedImageLabels struct {
	labels    map[string]string
	err       error
	expiresAt time.Time
}

var imageLabelCache sync.Map // map[string]cachedImageLabels

const imageLabelTTL = 5 * time.Minute

// FetchImageLabels reads an image's OCI labels from its registry (Docker
// Registry HTTP API v2): the manifest -- the linux/amd64 one of an index
// -- and then the config blob it names, which is where labels live.
// Anonymous Bearer token exchange is supported (GHCR's public images),
// and an image on Artifact Registry or GCR is read with this host's
// Google access token when it has one (registryauth.TokenFor); a
// registry that needs credentials beyond that answers with an error,
// which the Settings pane reports as an unverified contract rather than
// as an incompatible image.
//
// The token goes to the registry's own host only: a blob GET that the
// registry redirects to storage elsewhere is followed without it, which
// net/http does by itself for a redirect to another domain.
func FetchImageLabels(ctx context.Context, imageRef string) (map[string]string, error) {
	host, repo, ref, err := parseOCIImageReference(imageRef)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r := &registryReader{client: &http.Client{Timeout: 10 * time.Second, Transport: registryTransport}, base: "https://" + host + "/v2/" + repo, token: registryauth.TokenFor(ctx, host)}
	if isLoopbackRegistryHost(host) {
		r.base = "http://" + host + "/v2/" + repo
	}

	var manifest struct {
		MediaType string `json:"mediaType"`
		Config    struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := r.getJSON(ctx, "/manifests/"+ref, manifestAccept, &manifest); err != nil {
		return nil, err
	}
	if len(manifest.Manifests) > 0 {
		digest := manifest.Manifests[0].Digest
		for _, m := range manifest.Manifests {
			if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
				digest = m.Digest
				break
			}
		}
		if err := r.getJSON(ctx, "/manifests/"+digest, manifestAccept, &manifest); err != nil {
			return nil, err
		}
	}
	if manifest.Config.Digest == "" {
		return nil, fmt.Errorf("the manifest of %s names no config blob", imageRef)
	}
	var config struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := r.getJSON(ctx, "/blobs/"+manifest.Config.Digest, "*/*", &config); err != nil {
		return nil, err
	}
	if config.Config.Labels == nil {
		return map[string]string{}, nil
	}
	return config.Config.Labels, nil
}

var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// registryTransport is what FetchImageLabels and CheckImageManifest
// reach a registry through: nil, net/http's default, except in tests.
var registryTransport http.RoundTripper

// registryReader is one repository's v2 API, holding the token its
// requests carry: a Google access token from the start, for a Google
// registry, or else the anonymous one the first challenge handed out.
type registryReader struct {
	client    *http.Client
	base      string
	token     string
	exchanged bool
}

func (r *registryReader) getJSON(ctx context.Context, path, accept string, into any) error {
	do := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		return r.client.Do(req)
	}
	resp, err := do()
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && !r.exchanged {
		r.exchanged = true
		chal := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		tok, tokErr := fetchRegistryBearerToken(ctx, r.client, chal)
		if tokErr != nil {
			return fmt.Errorf("the registry wants credentials (%v)", tokErr)
		}
		r.token = tok
		if resp, err = do(); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned HTTP %d", r.base+path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}
