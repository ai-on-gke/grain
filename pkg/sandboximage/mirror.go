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

package sandboximage

import (
	"strings"

	"github.com/ai-on-gke/grain/pkg/k8s"
)

// MirrorHint is a configured image that a kubernetes-granule deployment
// with no sandbox_image_mirror reads as the repository to mirror its
// stamped guest into, rather than as an image to boot.
//
// It is how a k8s_granule_image of
// "us-docker.pkg.dev/<project>/grain/guest:sha-<old>@sha256:..." ends up
// ignored as an image and load-bearing as the mirror target: clearing it
// silently drops the cross-project mirror at the next restart. Settings
// and `grain state check` name it so that nobody has to work that out
// from the daemon's source.
type MirrorHint struct {
	// Field is the setting the repository was derived from: the
	// "k8s_granule_image" column, or the daemon's "-sandbox-image" flag.
	Field string
	// Image is that column's value.
	Image string
	// Repository is the mirror repository derived from it, without tag
	// or digest.
	Repository string
}

// DerivedMirror is the mirror repository a kubernetes-granule
// deployment with no explicit sandbox_image_mirror derives from its
// k8s_granule_image or its daemon's -sandbox-image flag (checked in that
// order; the flag is not in grain_config, so callers reading only the
// stored row pass ""). The custom sandbox_image column is never one: a
// value there that is a mirrored guest ref is ignored as an image
// instead (IsMirroredGuestRef). A mirror is derived from a bare
// Artifact Registry or GCR repository, or a "sha-" guest tag in a
// ".../guest" repository, in a GCP project -- anything but stampedRepo,
// the repository this build's own guest is published in. ok is false
// when neither names one.
func DerivedMirror(k8sGranuleImage, sandboxImageFlag, stampedRepo string) (MirrorHint, bool) {
	for _, candidate := range []struct{ field, image string }{
		{"k8s_granule_image", k8sGranuleImage},
		{"-sandbox-image", sandboxImageFlag},
	} {
		image := strings.TrimSpace(candidate.image)
		if image == "" {
			continue
		}
		repo := ImageRepository(image)
		_, project, _ := k8s.ImageRegistryProject(repo)
		if repo == "" || repo == stampedRepo || project == "" {
			continue
		}
		tag := ImageTag(image)
		if image == repo || ((strings.HasSuffix(repo, "/guest") || strings.HasSuffix(repo, "/sandbox")) && strings.HasPrefix(tag, "sha-")) {
			return MirrorHint{Field: candidate.field, Image: image, Repository: repo}, true
		}
	}
	return MirrorHint{}, false
}

// IsMirroredGuestRef reports whether ref is the bare mirror repository,
// or a guest tag inside it -- "sha-..." or stampedTag, the tag of the
// guest this build is stamped for -- which a kubernetes-granule
// deployment mirroring into mirrorRepo treats as the mirror rather than
// as an image of its own.
func IsMirroredGuestRef(ref, mirrorRepo, stampedTag string) bool {
	ref = strings.TrimSpace(ref)
	mirrorRepo = ImageRepository(strings.TrimSpace(mirrorRepo))
	if ref == "" || mirrorRepo == "" {
		return false
	}
	if ImageRepository(ref) != mirrorRepo {
		return false
	}
	tag := ImageTag(ref)
	return tag == "" || strings.HasPrefix(tag, "sha-") || tag == stampedTag
}

// ImageRepository is ref without its tag or digest. The last colon is
// only a tag separator when it comes after the last slash: a registry
// may carry a port ("127.0.0.1:5000/guest").
func ImageRepository(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		ref = ref[:colon]
	}
	return ref
}

// ImageTag is ref's tag, with any digest stripped, or "" for none.
func ImageTag(ref string) string {
	ref = strings.TrimSpace(ref)
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		return ref[colon+1:]
	}
	return ""
}
