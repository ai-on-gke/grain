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

package installtrack_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
)

func init() { installtracktest.Register() }

func TestDetectFallsBackToOSS(t *testing.T) {
	for _, p := range []installtrack.Probe{
		{},
		{Hostname: "devbox.example.com"},
		{Hostname: installtracktest.HostSuffix},
		{UnitText: "ExecStart=/usr/local/bin/grain daemon -agy-path /usr/local/bin/agy\n"},
	} {
		if got := p.Detect(); got.Track != installtrack.TrackOSS || len(got.Signals) != 0 {
			t.Errorf("%+v.Detect() = %+v, want oss with no signals", p, got)
		}
	}
}

// An edition that names its track as the build's own (SetBuildTrack) is
// what a signal-less host running its binary settles on, saying why; a
// host's own signals still say which install it is, and a build track
// nobody registered is not one.
func TestDetectFallsBackToTheBuildsOwnTrack(t *testing.T) {
	t.Cleanup(installtrack.SetBuildTrack(installtracktest.Track))
	if got := installtrack.BuildTrack(); got != installtracktest.Track {
		t.Fatalf("BuildTrack() = %q", got)
	}
	got := (installtrack.Probe{Hostname: "devbox.example.com"}).Detect()
	if got.Track != installtracktest.Track || !slices.Equal(got.Signals, []string{installtrack.BuildSignal(installtracktest.Track)}) {
		t.Errorf("signal-less Detect() = %+v, want %s from the build", got, installtracktest.Track)
	}
	got = (installtrack.Probe{Hostname: "box" + installtracktest.HostSuffix}).Detect()
	if got.Track != installtracktest.Track || !slices.Equal(got.Signals, []string{installtracktest.Signal}) {
		t.Errorf("Detect() with the host's signal = %+v, want the host's signal alone", got)
	}

	installtrack.SetBuildTrack("unregistered")
	if got := (installtrack.Probe{}).Detect(); got.Track != installtrack.TrackOSS || len(got.Signals) != 0 {
		t.Errorf("Detect() on an unregistered build track = %+v, want oss", got)
	}
}

func TestDetectReportsARegisteredTracksSignals(t *testing.T) {
	got := (installtrack.Probe{Hostname: "Box" + installtracktest.HostSuffix + "."}).Detect()
	if got.Track != installtracktest.Track || !slices.Equal(got.Signals, []string{installtracktest.Signal}) {
		t.Fatalf("Detect() = %+v", got)
	}
	// A unit's -agy-path is read from the unit file's text.
	unit := "[Service]\nExecStart=/usr/local/bin/grain daemon \\\n  -agy-path " + installtracktest.AgyPath + " \\\n  -ui-allowed-hosts grain.example.com\n"
	got = (installtrack.Probe{UnitText: unit}).Detect()
	if got.Track != installtracktest.Track || !slices.Contains(got.Signals, installtracktest.SignalAgyPath) {
		t.Fatalf("Detect() from the unit = %+v", got)
	}
	if tracks := installtrack.Tracks(); tracks[0] != installtrack.TrackOSS || !slices.Contains(tracks, installtracktest.Track) {
		t.Errorf("Tracks() = %v", tracks)
	}
}

func TestRepositoryTransitionRules(t *testing.T) {
	t.Setenv("GRAIN_ALLOWED_IMAGE_REPOS", "")
	t.Setenv(installtrack.OSSImageRepoEnv, "")
	t.Setenv(installtracktest.RepositoryEnv, "")
	if got := installtrack.DefaultRepository(installtrack.TrackOSS); got != installtrack.DefaultOSSGrainRepository {
		t.Errorf("DefaultRepository(oss) = %q", got)
	}
	if got := installtrack.DefaultRepository(installtracktest.Track); got != installtracktest.Repository {
		t.Errorf("DefaultRepository(acme) = %q", got)
	}
	if got := installtrack.DefaultRepository("unknown"); got != installtrack.DefaultOSSGrainRepository {
		t.Errorf("DefaultRepository(unknown) = %q", got)
	}
	for _, c := range []struct {
		from, to string
		ok       bool
	}{
		{installtrack.LegacyGrainRepository, installtracktest.Repository, true},
		{installtrack.LegacyGrainRepository, installtracktest.MovedRepository, true},
		{installtracktest.Repository, installtracktest.OptInRepository, true},
		{installtracktest.OptInRepository, installtracktest.Repository, true},
		{installtrack.LegacyGrainRepository, installtrack.DefaultOSSGrainRepository, true},
		{installtrack.LegacyGrainRepository, "registry.example/corp-eng/grain", false},
		{installtrack.LegacyGrainRepository, "evil.example/grain", false},
		{"ghcr.io/someone-else/grain/grain", installtrack.DefaultOSSGrainRepository, false},
	} {
		if got := installtrack.CanTransitionRepository(c.from, c.to); got != c.ok {
			t.Errorf("CanTransitionRepository(%q, %q) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
	if !installtrack.CanTransitionRepository(installtrack.LegacyGrainRepository, "registry.example/corp-eng/grain", "registry.example/corp-eng/grain") {
		t.Error("an explicitly allowed successor repository was refused")
	}
}

func TestRepositoryForTrack(t *testing.T) {
	t.Setenv(installtrack.OSSImageRepoEnv, "")
	for _, c := range []struct {
		repo  string
		track installtrack.Track
		want  string
	}{
		{installtrack.LegacyGrainRepository, installtracktest.Track, installtracktest.Repository},
		{installtrack.LegacyGrainRepository, installtrack.TrackOSS, installtrack.DefaultOSSGrainRepository},
		{"ghcr.io/ai-on-gke/grain/grain", installtrack.TrackOSS, installtrack.DefaultOSSGrainRepository},
		{installtrack.LegacyGrainRepository, "", installtrack.LegacyGrainRepository},
		{installtracktest.MovedRepository, installtracktest.Track, installtracktest.Repository},
		{installtracktest.MovedRepository, installtrack.TrackOSS, installtracktest.MovedRepository},
		{installtracktest.OptInRepository, installtracktest.Track, installtracktest.OptInRepository},
		{"registry.example.com/grain", installtracktest.Track, "registry.example.com/grain"},
	} {
		if got := installtrack.RepositoryForTrack(c.repo, c.track); got != c.want {
			t.Errorf("RepositoryForTrack(%q, %q) = %q, want %q", c.repo, c.track, got, c.want)
		}
	}
}

func TestLegacyGHCRToDockerHubAliasTable(t *testing.T) {
	cases := []struct {
		legacy      string
		wantOSS     string
		wantPrimary bool
	}{
		{"ghcr.io/bwsalmon/grain/grain", "docker.io/grainai/grain", true},
		{"ghcr.io/ai-on-gke/grain/grain", "docker.io/grainai/grain", false},
		{"ghcr.io/bwsalmon/grain/guest", "docker.io/grainai/sandbox", true},
		{"ghcr.io/bwsalmon/grain/granule-sandbox", "docker.io/grainai/sandbox", false},
		{"ghcr.io/ai-on-gke/grain/guest", "docker.io/grainai/sandbox", false},
		{"ghcr.io/bwsalmon/grain/guest-base", "docker.io/grainai/sandbox-base", true},
		{"ghcr.io/ai-on-gke/grain/guest-base", "docker.io/grainai/sandbox-base", false},
		{"ghcr.io/bwsalmon/grain/k8s-sandbox", "docker.io/grainai/sandbox-k8s", true},
		{"ghcr.io/ai-on-gke/grain/k8s-sandbox", "docker.io/grainai/sandbox-k8s", false},
		{"ghcr.io/bwsalmon/grain/k8s-sandbox-base", "docker.io/grainai/sandbox-k8s-base", true},
		{"ghcr.io/ai-on-gke/grain/k8s-sandbox-base", "docker.io/grainai/sandbox-k8s-base", false},
	}
	for _, tc := range cases {
		if !installtrack.IsLegacyRepository(tc.legacy) {
			t.Errorf("IsLegacyRepository(%q) = false, want true", tc.legacy)
		}
		if got := installtrack.ResolveLegacyRepository(tc.legacy); got != tc.wantOSS {
			t.Errorf("ResolveLegacyRepository(%q) = %q, want %q", tc.legacy, got, tc.wantOSS)
		}
		if tc.wantPrimary {
			leg, ok := installtrack.LegacyRepositoryFor(tc.wantOSS)
			if !ok || leg != tc.legacy {
				t.Errorf("LegacyRepositoryFor(%q) = (%q, %v), want (%q, true)", tc.wantOSS, leg, ok, tc.legacy)
			}
		}
		ref, changed := installtrack.ResolveLegacyImageRef(tc.legacy + ":v3.1.0")
		if !changed || ref != tc.wantOSS+":v3.1.0" {
			t.Errorf("ResolveLegacyImageRef(%q) = (%q, %v), want (%q, true)", tc.legacy+":v3.1.0", ref, changed, tc.wantOSS+":v3.1.0")
		}
	}
	// Short Docker Hub names normalize cleanly without reporting as legacy GHCR.
	if got := installtrack.ResolveLegacyRepository("grainai/sandbox"); got != "docker.io/grainai/sandbox" {
		t.Errorf("ResolveLegacyRepository(grainai/sandbox) = %q", got)
	}
	if installtrack.IsLegacyRepository("docker.io/grainai/grain") {
		t.Error("docker.io/grainai/grain reported as legacy repository")
	}
	// Digest-pinned legacy refs are not blindly rewritten onto Docker Hub.
	pinned := "ghcr.io/bwsalmon/grain/guest:sha-abc1234@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got, changed := installtrack.ResolveLegacyImageRef(pinned); changed || got != pinned {
		t.Errorf("ResolveLegacyImageRef(pinned) = (%q, %v), want unchanged", got, changed)
	}
}

func TestImageRepositoryHonoursTheTracksOverride(t *testing.T) {
	t.Setenv(installtrack.OSSImageRepoEnv, "registry.example/oss/grain")
	t.Setenv(installtracktest.RepositoryEnv, "registry.example/acme-mirror/grain")
	if got := installtrack.ImageRepository(installtrack.TrackOSS); got != "registry.example/oss/grain" {
		t.Errorf("ImageRepository(oss) = %q", got)
	}
	if got := installtrack.ImageRepository(installtracktest.Track); got != "registry.example/acme-mirror/grain" {
		t.Errorf("ImageRepository(acme) = %q", got)
	}
	if err := installtrack.ValidateTargetRepository("", "registry.example/acme-mirror/grain"); err != nil {
		t.Errorf("the track's override repository was refused: %v", err)
	}
}

func TestValidateTargetRepository(t *testing.T) {
	t.Setenv("GRAIN_ALLOWED_IMAGE_REPOS", "")
	t.Setenv(installtrack.OSSImageRepoEnv, "")
	t.Setenv(installtracktest.RepositoryEnv, "")
	for _, c := range []struct {
		from, to string
		ok       bool
	}{
		{installtracktest.Repository, installtracktest.OptInRepository, true},
		{"", installtracktest.Repository, true},
		{installtracktest.Repository, "evil.example/grain", false},
		{"", "evil.example/grain", false},
		{installtracktest.Repository, "", false},
	} {
		err := installtrack.ValidateTargetRepository(c.from, c.to)
		if (err == nil) != c.ok {
			t.Errorf("ValidateTargetRepository(%q, %q) = %v, want ok=%v", c.from, c.to, err, c.ok)
		}
	}
}
