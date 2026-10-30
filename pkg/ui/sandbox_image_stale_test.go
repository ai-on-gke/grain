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
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

// stampedAt makes c a daemon whose build is stamped for sha-e5544d6, with
// every image manifest resolving, so that only the pin is in question.
func stampedAt(c *ui.Client) {
	c.Config.SandboxBackends = model.SandboxBackends()
	c.Config.DaemonVersion = func() version.Info {
		return version.Info{Release: "v2.3.0-dev.5+e5544d6", Revision: "e5544d6aa0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5"}
	}
	c.Config.CheckImageManifest = func(context.Context, string) error { return nil }
}

func putImages(t *testing.T, c *ui.Client, mutate func(*model.Config)) {
	t.Helper()
	cfg := model.Config{PollInterval: model.DefaultPollInterval}
	mutate(&cfg)
	if err := c.Store.PutConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

// A k8s_granule_image copied from an older build's stock guest into the
// deployment's own registry pins that build: Settings says so beside the
// field, naming both builds, and stops saying it once the pin matches.
func TestSettingsWarnAboutAStaleStockImagePin(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	stampedAt(c)
	putImages(t, c, func(cfg *model.Config) {
		cfg.K8sGranuleImage = "us-docker.pkg.dev/gke-adaptive-orchestration/grain/guest:sha-31d0973"
		cfg.K8sSandboxImage = "registry.example/team/k8s-sandbox:v1"
	})

	s, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sha-31d0973", "sha-e5544d6", "another build"} {
		if !strings.Contains(s.K8sGranuleImageWarning, want) {
			t.Errorf("K8sGranuleImageWarning = %q, want it to mention %q", s.K8sGranuleImageWarning, want)
		}
	}
	if s.K8sSandboxImageWarning != "" {
		t.Errorf("K8sSandboxImageWarning = %q for a team's own image, want none", s.K8sSandboxImageWarning)
	}

	putImages(t, c, func(cfg *model.Config) {
		cfg.K8sGranuleImage = "us-docker.pkg.dev/gke-adaptive-orchestration/grain/guest:sha-e5544d6"
	})
	if s, err = c.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if s.K8sGranuleImageWarning != "" {
		t.Errorf("K8sGranuleImageWarning = %q for a mirror of the stamped build, want none", s.K8sGranuleImageWarning)
	}
}

// The custom sandbox_image field gets the same verdict, beside its
// contract one, when the "custom" image is the stock one at another build.
func TestCustomSandboxImageWarnsWhenItIsAStaleStockPin(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	stampedAt(c)
	c.Config.InspectImageLabels = func(context.Context, string) (map[string]string, error) {
		return map[string]string{"dev.grain.sandbox.contract": "2", "dev.granule.guest.contract": "1"}, nil
	}
	putImages(t, c, func(cfg *model.Config) {
		cfg.SandboxBackend = model.SandboxBackendGranule
		cfg.SandboxImage = "us-docker.pkg.dev/proj/grain/guest:sha-31d0973"
	})
	s, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.SandboxImageCheck == nil || !strings.Contains(s.SandboxImageCheck.Stale, "sha-31d0973") {
		t.Fatalf("SandboxImageCheck = %+v, want a stale verdict naming sha-31d0973", s.SandboxImageCheck)
	}
	if !strings.Contains(s.SandboxImageWarning, "sha-e5544d6") {
		t.Errorf("SandboxImageWarning = %q, want it to name the stamped build", s.SandboxImageWarning)
	}
}

// System Health lists every stale pin, by setting name, so it is seen
// where a sandbox's odd behaviour is investigated and not only in Settings.
func TestGetSandboxHealthReportsStaleSandboxImagePins(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	stampedAt(c)
	c.Config.Sandboxes = fakeSandboxHealth{}
	putImages(t, c, func(cfg *model.Config) {
		cfg.K8sSandboxImage = "ghcr.io/bwsalmon/grain/k8s-sandbox:v2.1.0"
		cfg.K8sGranuleImage = "ghcr.io/bwsalmon/grain/guest:sha-e5544d6"
	})

	got := decode[struct {
		Warnings []string `json:"sandboxImageWarnings"`
	}](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if len(got.Warnings) != 1 || !strings.HasPrefix(got.Warnings[0], "k8s_sandbox_image: ") || !strings.Contains(got.Warnings[0], "v2.1.0") {
		t.Errorf("sandboxImageWarnings = %q, want one for k8s_sandbox_image naming v2.1.0", got.Warnings)
	}

	// A deployment with no pool to report on still says it: the pin is
	// in its settings, not in the pool.
	c.Config.Sandboxes = nil
	got = decode[struct {
		Warnings []string `json:"sandboxImageWarnings"`
	}](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if len(got.Warnings) != 1 {
		t.Errorf("sandboxImageWarnings with no pool = %q, want the same one", got.Warnings)
	}
}
