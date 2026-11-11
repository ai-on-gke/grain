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

// The Kubernetes path's revision label, read from the registry.
//
// The image path reads the label with `docker image inspect` on the image
// it has pulled. The Kubernetes path pulls nothing before a pod runs the
// image, and the check has to come before any pod does, so the label is
// read from the registry by the digest the verifier signed off on.
//
// That is covered by the signature without the label being in it. The
// release signs the manifest digest and nothing else (release-images.sh
// signs with no annotations, so cosign's payload carries no commit). The
// manifest names its config blob by digest, and the config blob carries
// the labels. So every document read below is hashed and held to the
// digest the one before it named, starting from the signed one: a
// registry that serves anything else is refused, not believed.
//
// A multi-platform index is the signed document when the release pushes
// one. Each platform's manifest is then read, and all of them have to
// name the same commit. Which platform a node pulls is the kubelet's
// choice, so a label on only one of them would not say what runs.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/registryauth"
)

const (
	mediaOCIIndex          = "application/vnd.oci.image.index.v1+json"
	mediaOCIManifest       = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerList        = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaDockerManifest    = "application/vnd.docker.distribution.manifest.v2+json"
	registryManifestAccept = mediaOCIIndex + ", " + mediaOCIManifest + ", " + mediaDockerList + ", " + mediaDockerManifest
)

// maxIndexPlatforms bounds the manifests an index may name; a release
// pushes two or three.
const maxIndexPlatforms = 16

// registryImageRevision is the org.opencontainers.image.revision label of
// ref, `repo[:tag]@sha256:...`, read from the registry and held to the
// digest.
func registryImageRevision(ctx context.Context, ref string) (string, error) {
	pinned, ok := digestRef(ref)
	if !ok {
		return "", fmt.Errorf("%s is not pinned to a digest, so its revision label cannot be read by one", ref)
	}
	repo, digest, _ := strings.Cut(pinned, "@")
	host, name := registryLocation(repo)
	r := &registryReader{host: host, name: name, token: registryauth.TokenFor(ctx, host)}

	body, err := r.get(ctx, "manifests", digest, registryManifestAccept)
	if err != nil {
		return "", err
	}
	var doc struct {
		MediaType string `json:"mediaType"`
		Config    struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			MediaType   string            `json:"mediaType"`
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
			Platform    struct {
				OS string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("reading the manifest of %s: %w", pinned, err)
	}
	if len(doc.Manifests) == 0 {
		return r.manifestRevision(ctx, digest, body)
	}
	if len(doc.Manifests) > maxIndexPlatforms {
		return "", fmt.Errorf("%s is an index of %d manifests, more than the %d this reads", pinned, len(doc.Manifests), maxIndexPlatforms)
	}
	revision, seen := "", false
	for _, m := range doc.Manifests {
		// Build attestations sit in the index as manifests of platform
		// unknown/unknown; no node runs them, and they carry no labels.
		if m.Platform.OS == "unknown" || m.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
			continue
		}
		child, err := r.get(ctx, "manifests", m.Digest, registryManifestAccept)
		if err != nil {
			return "", err
		}
		rev, err := r.manifestRevision(ctx, m.Digest, child)
		if err != nil {
			return "", err
		}
		// Held to the first platform's even when that names none: an
		// unlabelled platform beside a labelled one is a platform whose
		// commit nobody asked about.
		if seen && rev != revision {
			return "", fmt.Errorf("the platforms of %s name different commits (%q and %q), so which one would run cannot be said", pinned, revision, rev)
		}
		revision, seen = rev, true
	}
	if !seen {
		return "", fmt.Errorf("%s is an index naming no image a node would run", pinned)
	}
	return revision, nil
}

// registryReader reads one repository's documents, each held to its
// digest.
type registryReader struct {
	host, name string
	token      string
	exchanged  bool
	plain      bool
}

// manifestRevision is the revision label in the config blob that body,
// the manifest at digest, names.
func (r *registryReader) manifestRevision(ctx context.Context, digest string, body []byte) (string, error) {
	var m struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", fmt.Errorf("reading manifest %s: %w", digest, err)
	}
	if m.Config.Digest == "" {
		return "", fmt.Errorf("manifest %s names no config blob", digest)
	}
	blob, err := r.get(ctx, "blobs", m.Config.Digest, "")
	if err != nil {
		return "", err
	}
	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return "", fmt.Errorf("reading config blob %s: %w", m.Config.Digest, err)
	}
	return strings.TrimSpace(cfg.Config.Labels["org.opencontainers.image.revision"]), nil
}

// get reads /v2/<name>/<kind>/<digest> and refuses it unless it hashes to
// digest.
func (r *registryReader) get(ctx context.Context, kind, digest, accept string) ([]byte, error) {
	want, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(want) != sha256.Size*2 {
		return nil, fmt.Errorf("%q is not a sha256 digest this can check what the registry serves against", digest)
	}
	var header http.Header
	if accept != "" {
		header = http.Header{"Accept": {accept}}
	}
	for {
		scheme := "https://"
		if r.plain {
			scheme = "http://"
		}
		target := scheme + r.host + "/v2/" + r.name + "/" + kind + "/" + digest
		resp, err := registryDo(ctx, http.MethodGet, target, r.token, header)
		if err != nil && !r.plain && errors.Is(err, http.ErrSchemeMismatch) && loopbackRegistry(r.host) {
			// ListRegistryTags's rule: a registry on this machine may
			// speak plain HTTP.
			r.plain = true
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s %s from %s: %w", kind, digest, r.host, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && !r.exchanged {
			r.exchanged = true
			challenge := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			if r.token, err = registryToken(ctx, challenge); err != nil {
				return nil, fmt.Errorf("reading %s %s from %s: %w", kind, digest, r.host, err)
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			return nil, fmt.Errorf("reading %s %s: %s answered %s", kind, digest, resp.Request.URL.Host, resp.Status)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
		if err != nil {
			return nil, fmt.Errorf("reading %s %s from %s: %w", kind, digest, r.host, err)
		}
		if len(body) > 8<<20 {
			return nil, fmt.Errorf("%s %s from %s is over 8 MiB", kind, digest, r.host)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != want {
			return nil, fmt.Errorf("%s served sha256:%s for %s %s, so what it says the image carries is not what was signed", r.host, got, kind, digest)
		}
		return body, nil
	}
}
