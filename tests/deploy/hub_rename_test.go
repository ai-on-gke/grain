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

package deploy

import (
	"strings"
	"testing"
)

// The Grain Registry became the Grain Hub: its image is built from
// Dockerfile.hub as grainai/hub, and for one release it is also published
// as grainai/registry, with the same tags, so a deployment still pulling
// that keeps upgrading. A release cut before the rename (Dockerfile.registry,
// build_registry_image) is still promoted as it was:
// promote_hub_image_test.go runs those trees.

// hubReleaseFiles are what a release's tree carries since the rename.
var hubReleaseFiles = map[string]string{
	"Dockerfile.hub":            "FROM scratch\nARG RELEASE=\nARG MAIN_GRAIN_HUB=\n",
	"Dockerfile.registry":       "FROM scratch\nARG RELEASE=\nARG MAIN_GRAIN_HUB=\n",
	"scripts/release-images.sh": "build_hub_image() {\n  :\n}\n",
}

// A promotion of a release cut since the rename rebuilds hub from its own
// Dockerfile.hub and ./cmd/grain-hub, and publishes the same image under
// registry too.
func TestPromoteOfAReleaseSinceTheHubRenamePublishesHubAndRegistry(t *testing.T) {
	remote, commits := releaseRepoWithFiles(t, postSplitDockerfile, true, hubReleaseFiles)
	short := commits["released"][:7]

	out, docker, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err != nil {
		t.Fatalf("promote --dry-run failed: %v\n%s", err, out)
	}
	containsRun(t, out, "and rebuild docker.io/grainai/hub:sha-"+short+" from "+commits["released"]+
		" (./cmd/grain-hub) with RELEASE=v1.5.0 as v1.5.0 and latest, unless docker.io/grainai/hub:v1.5.0 already reports v1.5.0")
	containsRun(t, out, "and publish the same image as docker.io/grainai/registry:v1.5.0 and latest (its pre-rename name, for one release)")
	if docker != "" {
		t.Errorf("a dry run asked docker for something:\n%s", docker)
	}
}

// Every merge's build tags the hub image under both names, and the push
// publishes, signs and moves the branch tag of both.
func TestReleaseImagesPublishesTheHubImageUnderBothNames(t *testing.T) {
	script := read(t, "scripts", "release-images.sh")
	contains(t, script, `hub_image="$root/hub:sha-$short"`)
	contains(t, script, `registry_image="$root/registry:sha-$short"`)
	build := body(t, script, `build_hub_image "$hub_image" "$hub_main"`)
	contains(t, build, `docker tag "$hub_image" "$registry_image"`)
	contains(t, script, `"$hub_image $root/hub:$branch"`)
	contains(t, script, `"$registry_image $root/registry:$branch"`)
	contains(t, script, `push_image "$hub_image"`)
	contains(t, script, `sign_image "$hub_image"`)
	contains(t, script, `push_image "$registry_image"`)
	contains(t, script, `sign_image "$registry_image"`)
}

// release.yml's on-demand guard knows both shapes of tree.
func TestReleaseWorkflowKnowsHubAndRegistryTrees(t *testing.T) {
	wf := read(t, ".github", "workflows", "release.yml")
	contains(t, wf, `git cat-file -e "${COMMIT}:Dockerfile.hub"`)
	contains(t, wf, `grep -q '^build_hub_image()'`)
	contains(t, wf, `images+=(hub registry)`)
	contains(t, wf, `git cat-file -e "${COMMIT}:Dockerfile.registry"`)
	contains(t, wf, `grep -q '^build_registry_image()'`)
}

// Dockerfile.registry is kept for one release so a deployment building
// with -f Dockerfile.registry keeps working; it is Dockerfile.hub
// verbatim below its deprecation note, so the two cannot drift.
func TestDockerfileRegistryIsDockerfileHub(t *testing.T) {
	hub := read(t, "Dockerfile.hub")
	reg := read(t, "Dockerfile.registry")
	const marker = "# Multi-stage build for the grain-hub container image"
	hi, ri := strings.Index(hub, marker), strings.Index(reg, marker)
	if hi < 0 || ri < 0 {
		t.Fatalf("Dockerfile.hub or Dockerfile.registry lacks %q", marker)
	}
	if hub[hi:] != reg[ri:] {
		t.Fatalf("Dockerfile.registry has drifted from Dockerfile.hub: copy Dockerfile.hub over everything below its deprecation note")
	}
	contains(t, reg[:ri], "DEPRECATED")
	contains(t, hub, `ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/grain-hub"]`)
	contains(t, hub, "RUN ln -s grain-hub /usr/local/bin/grain-registry")
	contains(t, hub, `"${MAIN_GRAIN_HUB:-${MAIN_GRAIN_REGISTRY:-./cmd/grain-hub}}"`)
}

// `make image-registry` and IMAGE_REGISTRY / MAIN_grain-registry still
// work for a release.
func TestMakefileKeepsTheRegistryNamesAsAliases(t *testing.T) {
	mk := read(t, "Makefile")
	contains(t, mk, "image-registry: image-hub")
	contains(t, mk, "IMAGE_HUB ?= $(or $(IMAGE_REGISTRY),grain-hub:dev)")
	contains(t, mk, "MAIN_grain-hub ?= $(or $(MAIN_grain-registry),./cmd/grain-hub)")
	contains(t, mk, "-t $(IMAGE_HUB) -f Dockerfile.hub .")
}
