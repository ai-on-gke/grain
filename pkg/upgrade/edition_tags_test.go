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
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
	"github.com/ai-on-gke/grain/pkg/version"
)

// linkEdition makes this process look like a binary with edition linked in
// (version.RegisterEdition from the edition's init), for the length of the
// test. version.Get is computed once per process, so it is swapped for one
// that reports the registered edition too.
func linkEdition(t *testing.T, edition string) {
	t.Helper()
	savedGet := version.Get
	version.RegisterEdition(edition)
	version.Get = func() version.Info {
		info := savedGet()
		info.Release, info.Edition = "", edition
		return info
	}
	t.Cleanup(func() {
		version.RegisterEdition("")
		version.Get = savedGet
	})
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

// TestDedicatedEditionRegistryOffersBareTags verifies that an edition-linked
// daemon on its own registered track (e.g. an Artifact Registry repository
// dedicated to that edition) accepts bare vX.Y.Z and vX.Y.Z-rc.N tags for
// Targets, LatestRelease, DiscardedRC and RetractedRelease, while shared
// registries still filter by edition suffix.
func TestDedicatedEditionRegistryOffersBareTags(t *testing.T) {
	linkEdition(t, "google")
	tags := []string{"main", "latest", "candidate", "v3.2.0", "v3.1.0", "v3.0.0-other"}
	list := func(context.Context, string) ([]string, error) { return tags, nil }

	dir := t.TempDir()
	u := newUpgrader(t, Config{
		StatusFile: filepath.Join(dir, "status.json"),
		Image: &ImageConfig{
			Track:      installtracktest.Track,
			Repository: installtracktest.Repository,
			RefFile:    filepath.Join(dir, "image.env"),
		},
		ListTags: list,
	})
	if got, want := targetVersions(u.Targets(context.Background())), []string{"v3.2.0", "v3.1.0"}; !slices.Equal(got, want) {
		t.Errorf("Targets versions = %v, want %v", got, want)
	}
	if latest, err := u.LatestRelease(context.Background()); err != nil || latest != "v3.2.0" {
		t.Errorf("LatestRelease = (%q, %v), want (v3.2.0, nil)", latest, err)
	}

	// Shared open-source and legacy GHCR repositories keep edition-suffix filtering.
	for _, sharedRepo := range []string{installtrack.DefaultOSSGrainRepository, installtrack.LegacyGrainRepository} {
		sharedUp := newUpgrader(t, Config{
			StatusFile: filepath.Join(dir, "status-shared.json"),
			Image: &ImageConfig{
				Repository: sharedRepo,
				RefFile:    filepath.Join(dir, "image.env"),
			},
			ListTags: func(context.Context, string) ([]string, error) {
				return []string{"v3.2.0", "v3.1.0-google", "v3.0.0"}, nil
			},
		})
		if got, want := targetVersions(sharedUp.Targets(context.Background())), []string{"v3.1.0-google"}; !slices.Equal(got, want) {
			t.Errorf("shared %s: Targets versions = %v, want %v", sharedRepo, got, want)
		}
	}

	// Discarded RC and retracted release on dedicated edition registry.
	rcUp := newUpgrader(t, Config{
		StatusFile:     filepath.Join(dir, "status-rc.json"),
		RunningRelease: func() string { return "v3.2.0-rc.1" },
		Image: &ImageConfig{
			Track:      installtracktest.Track,
			Repository: installtracktest.Repository,
			RefFile:    filepath.Join(dir, "image.env"),
		},
		ListTags: func(context.Context, string) ([]string, error) {
			return []string{"v3.1.0", "v3.2.0-rc.1", "v3.2.0-rc.1-discarded", "v3.2.0-rc.2"}, nil
		},
	})
	wantRC := &DiscardedRC{Running: "v3.2.0-rc.1", Latest: "v3.1.0", ReplacementRC: "v3.2.0-rc.2"}
	if got := rcUp.DiscardedRC(context.Background()); !reflect.DeepEqual(got, wantRC) {
		t.Errorf("DiscardedRC = %+v, want %+v", got, wantRC)
	}

	retUp := newUpgrader(t, Config{
		StatusFile:     filepath.Join(dir, "status-ret.json"),
		RunningRelease: func() string { return "v3.2.0" },
		Image: &ImageConfig{
			Track:      installtracktest.Track,
			Repository: installtracktest.Repository,
			RefFile:    filepath.Join(dir, "image.env"),
		},
		ListTags: func(context.Context, string) ([]string, error) {
			return []string{"v3.1.0", "v3.2.0", "v3.2.0-retracted"}, nil
		},
	})
	wantRet := &RetractedRelease{Running: "v3.2.0", Latest: "v3.1.0"}
	if got := retUp.Targets(context.Background()).RetractedRelease; !reflect.DeepEqual(got, wantRet) {
		t.Errorf("Targets().RetractedRelease = %+v, want %+v", got, wantRet)
	}
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
	rcTags := []string{"v3.2.0-rc.1", "v3.2.0-rc.2"}
	if got := discardedRCFromTags("v3.2.0-rc.1", rcTags, tagFilter{edition: "google"}); got != nil {
		t.Errorf("shared: discardedRCFromTags = %+v, want nil", got)
	}
	if got := discardedRCFromTags("v3.2.0-rc.1", rcTags, tagFilter{edition: "google", bareOK: true}); got == nil || got.ReplacementRC != "v3.2.0-rc.2" {
		t.Errorf("dedicated: discardedRCFromTags = %+v, want replacement v3.2.0-rc.2", got)
	}
}
