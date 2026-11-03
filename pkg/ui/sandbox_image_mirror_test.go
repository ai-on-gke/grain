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

package ui_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A kubernetes-granule deployment whose k8s_granule_image is a "sha-"
// guest pin in another project's .../guest repository, with no
// sandbox_image_mirror, mirrors the stamped guest into that repository
// and never boots the pin (pkg/daemon's sandboxImageMirrorRepo). On
// 2026-10-30 nobody could see that: the pane showed the pin, called it a
// stale stock pin, and two tasks set out to clear it -- which would have
// dropped the mirror at the next restart. The pane says what the pin is
// and what sandboxes boot, and a save clearing it keeps the mirror.
func TestSettingsReportsAndKeepsAMirrorDerivedFromK8sGranuleImage(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	stampedAt(c)
	const repo = "us-docker.pkg.dev/example-project/grain/guest"
	const pin = repo + ":sha-31d0973@sha256:d210757e35832d3c8f47f11abd84c3d2f82fa0b7b2c88a13fd021d6999018a83"
	putImages(t, c, func(cfg *model.Config) {
		cfg.MaxWorkers = 1
		cfg.GitHubHost = "github.com"
		cfg.SandboxBackend = model.SandboxBackendKubernetesGranule
		cfg.K8sGranuleImage = pin
	})

	s, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantEffective := repo + ":sha-e5544d6"
	if s.K8sGranuleImage != pin || s.K8sGranuleImageMirror != repo {
		t.Errorf("K8sGranuleImage, K8sGranuleImageMirror = %q, %q; want the stored pin, treated as %q", s.K8sGranuleImage, s.K8sGranuleImageMirror, repo)
	}
	if s.K8sGranuleImageEffective != wantEffective {
		t.Errorf("K8sGranuleImageEffective = %q, want the stamped guest mirrored, %q", s.K8sGranuleImageEffective, wantEffective)
	}
	if s.SandboxImageMirror != "" || s.SandboxImageMirrorEffective != repo || s.SandboxImageMirrorDerivedFrom != "k8s_granule_image" {
		t.Errorf("SandboxImageMirror, Effective, DerivedFrom = %q, %q, %q; want \"\", %q, k8s_granule_image",
			s.SandboxImageMirror, s.SandboxImageMirrorEffective, s.SandboxImageMirrorDerivedFrom, repo)
	}
	if s.K8sGranuleImageWarning != "" {
		t.Errorf("K8sGranuleImageWarning = %q, want no stale-pin warning for a pin that is never booted", s.K8sGranuleImageWarning)
	}

	cleared := ""
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{K8sGranuleImage: &cleared})
	if err != nil {
		t.Fatal(err)
	}
	if saved.K8sGranuleImage != "" || saved.SandboxImageMirror != repo {
		t.Errorf("after clearing: K8sGranuleImage, SandboxImageMirror = %q, %q; want \"\", the mirror kept explicitly as %q",
			saved.K8sGranuleImage, saved.SandboxImageMirror, repo)
	}
	if saved.K8sGranuleImageEffective != wantEffective || saved.SandboxImageMirrorDerivedFrom != "" || saved.K8sGranuleImageMirror != "" {
		t.Errorf("after clearing: effective %q, derived from %q, pin mirror %q; want %q from an explicit mirror",
			saved.K8sGranuleImageEffective, saved.SandboxImageMirrorDerivedFrom, saved.K8sGranuleImageMirror, wantEffective)
	}

	// An explicit mirror the operator then clears is theirs to clear.
	saved, err = c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxImageMirror: &cleared})
	if err != nil {
		t.Fatal(err)
	}
	if saved.SandboxImageMirror != "" || saved.SandboxImageMirrorEffective != "" {
		t.Errorf("after clearing the mirror: %q, %q; want both empty", saved.SandboxImageMirror, saved.SandboxImageMirrorEffective)
	}
}

// A pin that is an image of its own -- not a mirrored guest ref -- is
// shown as what sandboxes boot, and clearing it touches nothing else.
func TestClearingAK8sGranuleImageOfItsOwnLeavesTheMirrorAlone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	stampedAt(c)
	const pin = "us-docker.pkg.dev/example-project/team/custom-guest:v1"
	putImages(t, c, func(cfg *model.Config) {
		cfg.MaxWorkers = 1
		cfg.GitHubHost = "github.com"
		cfg.SandboxBackend = model.SandboxBackendKubernetesGranule
		cfg.K8sGranuleImage = pin
	})
	s, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.K8sGranuleImageEffective != pin || s.K8sGranuleImageMirror != "" || s.SandboxImageMirrorEffective != "" {
		t.Errorf("effective %q, pin mirror %q, mirror %q; want the pin booted and no mirror", s.K8sGranuleImageEffective, s.K8sGranuleImageMirror, s.SandboxImageMirrorEffective)
	}
	cleared := ""
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{K8sGranuleImage: &cleared})
	if err != nil {
		t.Fatal(err)
	}
	if saved.SandboxImageMirror != "" {
		t.Errorf("SandboxImageMirror = %q after clearing an image of its own, want it left empty", saved.SandboxImageMirror)
	}
}
