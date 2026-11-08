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

//go:build ignore

// item2_alias_probe exercises §9.2's legacy GHCR -> Docker Hub alias table
// across pkg/installtrack and pkg/upgrade, verifying seamless transition from
// ghcr.io/{bwsalmon,ai-on-gke}/grain/* onto docker.io/grainai/* while keeping
// digest-pinned legacy references and explicit private registries untouched.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

func mustEqual[T comparable](label string, got, want T) {
	if got != want {
		log.Fatalf("FAIL %s: got %v, want %v", label, got, want)
	}
	fmt.Printf("PASS %s -> %v\n", label, got)
}

func main() {
	repoCases := []struct {
		in     string
		want   string
		legacy bool
	}{
		{"ghcr.io/bwsalmon/grain/grain", "docker.io/grainai/grain", true},
		{"ghcr.io/ai-on-gke/grain/grain", "docker.io/grainai/grain", true},
		{"ghcr.io/bwsalmon/grain/guest", "docker.io/grainai/sandbox", true},
		{"ghcr.io/bwsalmon/grain/granule-sandbox", "docker.io/grainai/sandbox", true},
		{"ghcr.io/ai-on-gke/grain/guest", "docker.io/grainai/sandbox", true},
		{"ghcr.io/bwsalmon/grain/guest-base", "docker.io/grainai/sandbox-base", true},
		{"ghcr.io/bwsalmon/grain/k8s-sandbox", "docker.io/grainai/sandbox-k8s", true},
		{"ghcr.io/ai-on-gke/grain/k8s-sandbox", "docker.io/grainai/sandbox-k8s", true},
		{"ghcr.io/bwsalmon/grain/k8s-sandbox-base", "docker.io/grainai/sandbox-k8s-base", true},
		{"grainai/grain", "docker.io/grainai/grain", false},
		{"index.docker.io/grainai/registry", "docker.io/grainai/registry", false},
		{"registry-1.docker.io/grainai/sandbox", "docker.io/grainai/sandbox", false},
		{"us-docker.pkg.dev/my-proj/containers/grain", "us-docker.pkg.dev/my-proj/containers/grain", false},
		{"127.0.0.1:4281/custom/grain", "127.0.0.1:4281/custom/grain", false},
		{"ghcr.io/example-org/private-grain", "ghcr.io/example-org/private-grain", false},
	}
	for _, tc := range repoCases {
		mustEqual("ResolveLegacyRepository("+tc.in+")", installtrack.ResolveLegacyRepository(tc.in), tc.want)
		mustEqual("IsLegacyRepository("+tc.in+")", installtrack.IsLegacyRepository(tc.in), tc.legacy)
	}

	// Unpinned legacy image refs rewrite to Docker Hub; digest-pinned refs stay put.
	ref, changed := installtrack.ResolveLegacyImageRef("ghcr.io/ai-on-gke/grain/guest:v3.1.0")
	mustEqual("ResolveLegacyImageRef(unpinned).ref", ref, "docker.io/grainai/sandbox:v3.1.0")
	mustEqual("ResolveLegacyImageRef(unpinned).changed", changed, true)

	pinnedIn := "ghcr.io/bwsalmon/grain/guest:v3.1.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	pinnedOut, pinnedChanged := installtrack.ResolveLegacyImageRef(pinnedIn)
	mustEqual("ResolveLegacyImageRef(digest-pinned).ref", pinnedOut, pinnedIn)
	mustEqual("ResolveLegacyImageRef(digest-pinned).changed", pinnedChanged, false)

	customRef, customChanged := installtrack.ResolveLegacyImageRef("us-docker.pkg.dev/my-proj/containers/grain:latest")
	mustEqual("ResolveLegacyImageRef(custom-private).ref", customRef, "us-docker.pkg.dev/my-proj/containers/grain:latest")
	mustEqual("ResolveLegacyImageRef(custom-private).changed", customChanged, false)

	// Verify upgrade.New transitions a pre-cutover GHCR unit/env onto docker.io/grainai/grain
	// while keeping an explicit custom private registry untouched.
	tmp, err := os.MkdirTemp("", "item2-prober-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	legacyEnv := filepath.Join(tmp, "legacy-image.env")
	if err := os.WriteFile(legacyEnv, []byte("GRAIN_IMAGE=ghcr.io/ai-on-gke/grain/grain:latest\n"), 0o600); err != nil {
		log.Fatal(err)
	}
	legacyUpg := upgrade.New(upgrade.Config{
		StatusFile: filepath.Join(tmp, "legacy-status.json"),
		Image: &upgrade.ImageConfig{
			Repository: "ghcr.io/ai-on-gke/grain/grain",
			Track:      installtrack.TrackOSS,
			RefFile:    legacyEnv,
		},
	})
	legacyRun, err := legacyUpg.Running()
	if err != nil {
		log.Fatal(err)
	}
	mustEqual("Upgrader(legacy ghcr).Running().Ref", legacyRun.Ref, "ghcr.io/ai-on-gke/grain/grain:latest")
	mustEqual("Upgrader(legacy ghcr).EffectiveRepository()", legacyUpg.EffectiveRepository(), "docker.io/grainai/grain")

	customEnv := filepath.Join(tmp, "custom-image.env")
	if err := os.WriteFile(customEnv, []byte("GRAIN_IMAGE=us-docker.pkg.dev/my-proj/containers/grain:latest\n"), 0o600); err != nil {
		log.Fatal(err)
	}
	customUpg := upgrade.New(upgrade.Config{
		StatusFile: filepath.Join(tmp, "custom-status.json"),
		Image: &upgrade.ImageConfig{
			Repository: "us-docker.pkg.dev/my-proj/containers/grain",
			Track:      installtrack.TrackOSS,
			RefFile:    customEnv,
		},
	})
	customRun, err := customUpg.Running()
	if err != nil {
		log.Fatal(err)
	}
	mustEqual("Upgrader(custom private).Running().Ref", customRun.Ref, "us-docker.pkg.dev/my-proj/containers/grain:latest")
	mustEqual("Upgrader(custom private).EffectiveRepository()", customUpg.EffectiveRepository(), "us-docker.pkg.dev/my-proj/containers/grain")

	fmt.Println("ALL ITEM 2 ALIAS & UPGRADE TRANSITION CHECKS PASSED")
}
