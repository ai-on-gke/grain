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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/version"
)

// linkEdition makes this process look like a binary with edition linked in
// (version.RegisterEdition from the edition's init), for the length of the
// test. version.Get is computed once per process, so it is swapped for one
// that reports the registered edition too.
func linkEdition(t *testing.T, edition string) {
	t.Helper()
	savedGet := version.Get
	base := savedGet()
	version.RegisterEdition(edition)
	version.Get = func() version.Info {
		info := base
		info.Release, info.Edition = "", edition
		return info
	}
	t.Cleanup(func() {
		version.RegisterEdition("")
		version.Get = savedGet
	})
}

// editionForge is a release repository at github.com/owner/repo publishing
// the given release tags, each with a v1 images.json.
func editionForge(t *testing.T, owner, repo string, tags ...string) *fakeForgeServer {
	t.Helper()
	forge := newFakeForgeServer(t, owner, repo)
	for i, tag := range tags {
		publishRelease(forge, tag, testCommit1, "2026-11-01T10:00:00Z", int64(10+i))
	}
	return forge
}

// editionUpgrader is an image-pipeline Upgrader loading release assets
// from forge, on edition, with img's fields laid over the defaults.
func editionUpgrader(t *testing.T, forge *fakeForgeServer, edition string, img ImageConfig, running string) *Upgrader {
	t.Helper()
	dir := t.TempDir()
	if img.Repository == "" {
		img.Repository = "edition-local/grain"
	}
	img.RefFile = filepath.Join(dir, "image.env")
	img.PullCmd = []string{"false"}
	img.ReleaseAssets.ClientForHost = forge.clientFunc("", "", "")
	cfg := Config{
		Image:      &img,
		Edition:    edition,
		StatusFile: filepath.Join(dir, "status.json"),
	}
	if running != "" {
		cfg.RunningRelease = func() string { return running }
	}
	return newUpgrader(t, cfg)
}

func targetVersions(ts Targets) []string {
	var out []string
	for _, tg := range ts.Targets {
		if tg.Kind == TargetVersion {
			out = append(out, tg.Name)
		}
	}
	return out
}

// TestDedicatedEditionReleaseRepositoryOffersBareTags is the post-split
// edition release repository: an edition-linked daemon (edition "google")
// loading release assets from its own repository, which publishes bare
// vX.Y.Z tags, is offered them as versions and follows them as its latest
// release -- however the repository was named.
func TestDedicatedEditionReleaseRepositoryOffersBareTags(t *testing.T) {
	const track installtrack.Track = "test-edition-track"
	for _, tc := range []struct {
		name    string
		edition string // Config.Edition; "" leaves it to the linked edition
		img     ImageConfig
	}{
		{
			name:    "explicit release repository, Config.Edition",
			edition: "google",
			img:     ImageConfig{Source: ImageSourceReleaseAssets, ReleaseAssets: ReleaseAssetsConfig{Repository: "github.com/team/edition"}},
		},
		{
			name: "explicit release repository, linked edition",
			img:  ImageConfig{Source: ImageSourceReleaseAssets, ReleaseAssets: ReleaseAssetsConfig{Repository: "github.com/team/edition"}},
		},
		{
			name:    "track-registered release repository",
			edition: "google",
			img:     ImageConfig{Track: track},
		},
		{
			name:    "release repository inferred from a post-split image repository",
			edition: "google",
			img:     ImageConfig{Repository: "ghcr.io/team/edition/grain", Source: ImageSourceReleaseAssets},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linkEdition(t, "google")
			upgradeTrackDefaults(t, track, TrackImageDefaults{Source: ImageSourceReleaseAssets, ReleaseRepository: "github.com/team/edition"})
			forge := editionForge(t, "team", "edition", "v3.1.0", "v3.2.0", "v3.3.0-rc.1")
			u := editionUpgrader(t, forge, tc.edition, tc.img, "")
			if got := u.edition(); got != "google" {
				t.Fatalf("edition() = %q, want google", got)
			}
			if !u.dedicatedReleaseRepository() {
				t.Fatalf("release repository %q is not dedicated", u.EffectiveReleaseAssets().Repository)
			}
			latest, err := u.LatestRelease(context.Background())
			if err != nil || latest != "v3.2.0" {
				t.Errorf("LatestRelease = (%q, %v), want (v3.2.0, nil)", latest, err)
			}
			ts := u.Targets(context.Background())
			if got, want := targetVersions(ts), []string{"v3.2.0", "v3.1.0"}; !slices.Equal(got, want) {
				t.Errorf("Targets versions = %v (error %q), want %v", got, ts.VersionsError, want)
			}
		})
	}
}

// upgradeTrackDefaults registers d for track for the length of the test.
func upgradeTrackDefaults(t *testing.T, track installtrack.Track, d TrackImageDefaults) {
	t.Helper()
	RegisterTrackDefaults(track, d)
	t.Cleanup(func() { UnregisterTrackDefaults(track) })
}

// TestSharedReleaseRepositoryKeepsEditionSuffixes is the other side: core's
// release repository is shared with the legacy pre-split tags, where a bare
// vX.Y.Z is a core build, so an edition-linked daemon still takes only its
// own -google tags from it.
func TestSharedReleaseRepositoryKeepsEditionSuffixes(t *testing.T) {
	linkEdition(t, "google")
	forge := editionForge(t, "ai-on-gke", "grain", "v3.1.0", "v2.0.0-google", "v1.9.0-google")
	for _, img := range []ImageConfig{
		{Source: ImageSourceReleaseAssets, ReleaseAssets: ReleaseAssetsConfig{Repository: "github.com/ai-on-gke/grain"}},
		{Repository: installtrack.LegacyGrainRepository, Source: ImageSourceReleaseAssets},
	} {
		u := editionUpgrader(t, forge, "google", img, "")
		if u.dedicatedReleaseRepository() {
			t.Fatalf("release repository %q counted as dedicated", u.EffectiveReleaseAssets().Repository)
		}
		latest, err := u.LatestRelease(context.Background())
		if err != nil || latest != "v2.0.0-google" {
			t.Errorf("LatestRelease = (%q, %v), want (v2.0.0-google, nil)", latest, err)
		}
		if got, want := targetVersions(u.Targets(context.Background())), []string{"v2.0.0-google", "v1.9.0-google"}; !slices.Equal(got, want) {
			t.Errorf("Targets versions = %v, want %v", got, want)
		}
	}

	// A core daemon on the same repository takes the bare tags, as before.
	u := editionUpgrader(t, forge, "oss", ImageConfig{Source: ImageSourceReleaseAssets, ReleaseAssets: ReleaseAssetsConfig{Repository: "github.com/ai-on-gke/grain"}}, "")
	if latest, err := u.LatestRelease(context.Background()); err != nil || latest != "v3.1.0" {
		t.Errorf("core LatestRelease = (%q, %v), want (v3.1.0, nil)", latest, err)
	}
}

// TestDedicatedEditionReleaseRepositoryDiscardedAndRetracted: the
// discarded-candidate and retracted-release banners read the same bare
// tags on a dedicated edition repository.
func TestDedicatedEditionReleaseRepositoryDiscardedAndRetracted(t *testing.T) {
	linkEdition(t, "google")
	img := ImageConfig{Source: ImageSourceReleaseAssets, ReleaseAssets: ReleaseAssetsConfig{Repository: "github.com/team/edition"}}

	t.Run("a candidate replaced by a newer one is discarded", func(t *testing.T) {
		forge := editionForge(t, "team", "edition", "v3.1.0", "v3.2.0-rc.1", "v3.2.0-rc.2")
		u := editionUpgrader(t, forge, "google", img, "v3.2.0-rc.1")
		want := &DiscardedRC{Running: "v3.2.0-rc.1", Latest: "v3.1.0", ReplacementRC: "v3.2.0-rc.2"}
		if got := u.DiscardedRC(context.Background()); !reflect.DeepEqual(got, want) {
			t.Errorf("DiscardedRC = %+v, want %+v", got, want)
		}
		if got := u.Targets(context.Background()).DiscardedRC; !reflect.DeepEqual(got, want) {
			t.Errorf("Targets().DiscardedRC = %+v, want %+v", got, want)
		}
	})

	t.Run("a promoted candidate is not discarded", func(t *testing.T) {
		forge := editionForge(t, "team", "edition", "v3.1.0", "v3.2.0-rc.1", "v3.2.0-rc.2", "v3.2.0")
		u := editionUpgrader(t, forge, "google", img, "v3.2.0-rc.1")
		if got := u.DiscardedRC(context.Background()); got != nil {
			t.Errorf("DiscardedRC = %+v, want nil (v3.2.0 promoted)", got)
		}
	})

	t.Run("a retracted release points back at the previous one", func(t *testing.T) {
		forge := editionForge(t, "team", "edition", "v3.1.0", "v3.2.0")
		tagUnpublished(forge, "v3.2.0-retracted", testCommit1, "2026-11-02T10:00:00Z", 30)
		u := editionUpgrader(t, forge, "google", img, "v3.2.0")
		want := &RetractedRelease{Running: "v3.2.0", Latest: "v3.1.0"}
		if got := u.Targets(context.Background()).RetractedRelease; !reflect.DeepEqual(got, want) {
			t.Errorf("Targets().RetractedRelease = %+v, want %+v", got, want)
		}
		if got := u.CachedRetractedRelease(context.Background()); !reflect.DeepEqual(got, want) {
			t.Errorf("CachedRetractedRelease = %+v, want %+v", got, want)
		}
	})
}

// TestTagFilterBareTagsOnlyOnDedicatedRepositories pins the tag-level
// rule: bare tags join an edition's own only when the list came from a
// dedicated repository, and another edition's suffix never does.
func TestTagFilterBareTagsOnlyOnDedicatedRepositories(t *testing.T) {
	tags := []string{"v3.2.0", "v3.1.0-google", "v3.1.0-other", "v3.0.0"}
	for _, tc := range []struct {
		f    tagFilter
		want []string
	}{
		{tagFilter{edition: "google"}, []string{"v3.1.0-google"}},
		{tagFilter{edition: "google", bareOK: true}, []string{"v3.2.0", "v3.1.0-google", "v3.0.0"}},
		{tagFilter{}, []string{"v3.2.0", "v3.0.0"}},
	} {
		if got := tc.f.releasedVersions(tags); !slices.Equal(got, tc.want) {
			t.Errorf("%+v.releasedVersions = %v, want %v", tc.f, got, tc.want)
		}
	}
	// On a shared registry a google candidate is not discarded by a bare
	// (core) candidate of the same version; on a dedicated one it is.
	rcTags := []string{"v3.2.0-rc.1", "v3.2.0-rc.2"}
	if got := discardedRCFromTags("v3.2.0-rc.1", rcTags, tagFilter{edition: "google"}); got != nil {
		t.Errorf("shared: discardedRCFromTags = %+v, want nil", got)
	}
	if got := discardedRCFromTags("v3.2.0-rc.1", rcTags, tagFilter{edition: "google", bareOK: true}); got == nil || got.ReplacementRC != "v3.2.0-rc.2" {
		t.Errorf("dedicated: discardedRCFromTags = %+v, want replacement v3.2.0-rc.2", got)
	}
}

// TestDedicatedEditionRegistryFallbackKeepsEditionSuffixes: when a
// dedicated edition release repository publishes nothing and the tag list
// falls back to the image registry -- which may be shared between editions
// -- a bare vX.Y.Z there is not taken as the edition's release, so neither
// the selector nor the automatic upgrade (LatestRelease) offers a core
// build to an edition daemon.
func TestDedicatedEditionRegistryFallbackKeepsEditionSuffixes(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/team/grain/tags/list" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tags":["v3.1.0","v3.0.0-google","v2.9.0"]}`))
	}))
	defer srv.Close()
	registryHosts(t, map[string]*httptest.Server{"registry.example": srv})
	forge := editionForge(t, "team", "edition")
	u := editionUpgrader(t, forge, "google", ImageConfig{
		Repository:    "registry.example/team/grain",
		ReleaseAssets: ReleaseAssetsConfig{Repository: "github.com/team/edition"},
	}, "")
	if !u.dedicatedReleaseRepository() {
		t.Fatalf("release repository %q is not dedicated", u.EffectiveReleaseAssets().Repository)
	}
	if got := u.EffectiveImageFallback(); got != ImageSourceRegistry {
		t.Fatalf("EffectiveImageFallback = %q, want registry", got)
	}
	if latest, err := u.LatestRelease(context.Background()); err != nil || latest != "v3.0.0-google" {
		t.Errorf("LatestRelease = (%q, %v), want (v3.0.0-google, nil)", latest, err)
	}
	if got, want := targetVersions(u.Targets(context.Background())), []string{"v3.0.0-google"}; !slices.Equal(got, want) {
		t.Errorf("Targets versions = %v, want %v", got, want)
	}
}
