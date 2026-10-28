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

// What the Storage section of the System pane's Sandbox health tab is
// told about the rest of the volume (grain/task-276): the two
// directories grain CI keeps on it, and the sandboxes themselves.
//
// Through GET /api/sandboxes, which is the poll that draws the disk bars
// these figures are under: two readings of one volume taken from
// different moments disagree, and a figure nothing serves to that pane
// is one nobody sees.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ciStorageRunner is a ci.Runner that also knows what its artifacts
// occupy -- the two interfaces a real deployment's ciGate satisfies
// together, kept apart here because a runner that keeps artifacts it
// cannot measure is a real state and the section has to survive it.
type ciStorageRunner struct {
	*fakeCIRunner
	artifacts   ci.ArtifactStats
	keeps       bool
	artifactErr error
}

func (r *ciStorageRunner) ArtifactStats(context.Context) (ci.ArtifactStats, bool, error) {
	if !r.keeps {
		return ci.ArtifactStats{}, false, nil
	}
	return r.artifacts, true, r.artifactErr
}

// The reading an operator clearing space needs: how much each of CI's
// two directories holds, and the bounds each is held under -- so that
// "200 MiB" is read as "200 of 256" rather than as a number with nothing
// behind it.
func TestSandboxHealthReportsWhatCIKeepsOnTheVolume(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{}
	oldestEntry := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	oldestArtifact := time.Date(2026, 9, 15, 6, 0, 0, 0, time.UTC)
	client.Config.CI = &ciStorageRunner{
		fakeCIRunner: &fakeCIRunner{
			cache: true,
			stats: ci.CacheStats{
				Dir: "/var/lib/grain/data/ci-cache", Entries: 512, Bytes: 200 << 20,
				Oldest: oldestEntry, Hits: 80, Misses: 20,
				MaxAge: ci.DefaultCacheMaxAge, MaxBytes: ci.DefaultCacheMaxBytes,
				MaxEntries: ci.DefaultCacheMaxEntries,
			},
		},
		keeps: true,
		artifacts: ci.ArtifactStats{
			Dir: "/var/lib/grain/data/ci-artifacts", Runs: 6, Artifacts: 11, Bytes: 1 << 30,
			Oldest: oldestArtifact, MaxAge: ci.DefaultArtifactMaxAge,
			MaxBytes: ci.DefaultArtifactMaxBytes, MaxRunBytes: ci.DefaultArtifactMaxRunBytes,
			MaxArtifactBytes: ci.DefaultArtifactMaxArtifactBytes,
		},
	}

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)

	if got.CIJobCache == nil {
		t.Fatal("no job cache figures at all")
	}
	if got.CIJobCache.Bytes != 200<<20 || got.CIJobCache.Entries != 512 {
		t.Errorf("job cache = %+v, want what the cache holds", got.CIJobCache)
	}
	if got.CIJobCache.MaxBytes != ci.DefaultCacheMaxBytes || got.CIJobCache.MaxEntries != ci.DefaultCacheMaxEntries {
		t.Errorf("job cache bounds = %+v, want the bounds in force beside the reading", got.CIJobCache)
	}
	// The hit rate is what emptying it costs, which is the whole of why
	// that button asks first: it belongs with the size, not on another
	// pane.
	if got.CIJobCache.Hits != 80 || got.CIJobCache.Misses != 20 {
		t.Errorf("job cache lookups = %d hits, %d misses, want 80 and 20", got.CIJobCache.Hits, got.CIJobCache.Misses)
	}
	if got.CIJobCache.MaxAgeSeconds != ci.DefaultCacheMaxAge.Seconds() {
		t.Errorf("job cache max age = %v seconds, want %v", got.CIJobCache.MaxAgeSeconds, ci.DefaultCacheMaxAge.Seconds())
	}
	if got.CIJobCache.OldestAt == nil || !got.CIJobCache.OldestAt.Equal(oldestEntry) {
		t.Errorf("oldest entry = %v, want %v", got.CIJobCache.OldestAt, oldestEntry)
	}

	if got.CIArtifacts == nil {
		t.Fatal("no artifact figures at all")
	}
	if got.CIArtifacts.Bytes != 1<<30 || got.CIArtifacts.Runs != 6 || got.CIArtifacts.Artifacts != 11 {
		t.Errorf("artifacts = %+v, want what the store holds", got.CIArtifacts)
	}
	if got.CIArtifacts.MaxBytes != ci.DefaultArtifactMaxBytes ||
		got.CIArtifacts.MaxRunBytes != ci.DefaultArtifactMaxRunBytes ||
		got.CIArtifacts.MaxArtifactBytes != ci.DefaultArtifactMaxArtifactBytes {
		t.Errorf("artifact bounds = %+v, want all three ceilings beside the reading", got.CIArtifacts)
	}
	if got.CIArtifacts.Dir != "/var/lib/grain/data/ci-artifacts" {
		t.Errorf("artifact dir = %q, want where to go by hand -- there is no button", got.CIArtifacts.Dir)
	}
	if got.CIArtifacts.OldestAt == nil || !got.CIArtifacts.OldestAt.Equal(oldestArtifact) {
		t.Errorf("oldest artifact = %v, want %v", got.CIArtifacts.OldestAt, oldestArtifact)
	}
}

// Each half is absent on its own terms, and neither absence costs the
// other or the pane: a deployment running CI with no job cache behind it
// (every job runs, every time) still reports its artifacts, and a runner
// that cannot measure its artifacts still reports its cache.
func TestSandboxHealthReportsCIStorageItCannotMeasureAsAbsent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		runner        ci.Runner
		wantCache     bool
		wantArtifacts bool
	}{
		{
			name:   "no CI wired at all",
			runner: nil,
		},
		{
			name:          "CI with no job cache",
			runner:        &ciStorageRunner{fakeCIRunner: &fakeCIRunner{}, keeps: true},
			wantArtifacts: true,
		},
		{
			name:      "CI that keeps artifacts it cannot measure",
			runner:    &ciStorageRunner{fakeCIRunner: &fakeCIRunner{cache: true}, keeps: false},
			wantCache: true,
		},
		{
			// A runner that keeps no artifacts at all -- a test's fake, a
			// build with no artifact store wired -- answers ok=false
			// (ci.NopRunner) and the cache is still reported.
			name:      "a runner that cannot be asked about artifacts",
			runner:    &fakeCIRunner{cache: true},
			wantCache: true,
		},
		{
			name:   "an artifact store that has stopped answering",
			runner: &ciStorageRunner{fakeCIRunner: &fakeCIRunner{}, keeps: true, artifactErr: errFailedMeasurement},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, client := testServer(t)
			client.Config.Sandboxes = fakeSandboxHealth{}
			client.Config.CI = tc.runner

			got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
			if !got.Enabled {
				t.Fatal("the pane went down over a storage reading it could not take")
			}
			if (got.CIJobCache != nil) != tc.wantCache {
				t.Errorf("job cache = %+v, want present = %v", got.CIJobCache, tc.wantCache)
			}
			if (got.CIArtifacts != nil) != tc.wantArtifacts {
				t.Errorf("artifacts = %+v, want present = %v", got.CIArtifacts, tc.wantArtifacts)
			}
		})
	}
}

// The sandboxes are the largest consumer of that volume by far and had
// no figure anywhere. Summed out of the same sweep the sandbox table is
// drawn from, so that the total and the rows it is the total of cannot
// disagree.
func TestSandboxHealthReportsWhatTheLiveSandboxesHaveWritten(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{snapshots: []ui.SandboxSnapshot{
		{Sandbox: "task-1", DiskGB: 100, DiskUsedMB: 40 << 10, DiskTotalMB: 100 << 10, Ready: true},
		{Sandbox: "ci-job-1", DiskGB: 100, DiskUsedMB: 52 << 10, DiskTotalMB: 100 << 10, Ready: true},
		// Still starting, or no longer answering: 0/0 is how the backend
		// spells "no reading", and counting it as an empty disk would
		// make a pool of unreachable guests look like an empty volume.
		{Sandbox: "task-2", DiskGB: 100},
	}}

	got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if got.SandboxStorage == nil {
		t.Fatal("no sandbox storage figures at all")
	}
	if got.SandboxStorage.Sandboxes != 3 || got.SandboxStorage.Measured != 2 {
		t.Errorf("counts = %+v, want three live and two that answered", got.SandboxStorage)
	}
	if got.SandboxStorage.UsedMB != 92<<10 {
		t.Errorf("written = %d MB, want the two readings summed", got.SandboxStorage.UsedMB)
	}
	// What they were promised, beside what they have written: an overlay
	// grows as it is written, so the promise is the figure the reading
	// grows towards rather than a reservation taken out of the volume.
	if got.SandboxStorage.DeclaredGB != 300 {
		t.Errorf("declared = %d GB, want every live sandbox's own disk", got.SandboxStorage.DeclaredGB)
	}
}

// An idle deployment reports a reading of nothing rather than no
// reading: "nothing is in flight" is an answer to "what is eating this
// volume", and a missing figure is not.
func TestSandboxHealthReportsAnIdlePoolAsOccupyingNothing(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{}

	got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if got.SandboxStorage == nil {
		t.Fatal("an idle deployment reported no sandbox storage at all")
	}
	if got.SandboxStorage.Sandboxes != 0 || got.SandboxStorage.UsedMB != 0 {
		t.Errorf("idle pool = %+v, want nothing live and nothing written", got.SandboxStorage)
	}
}

// And a deployment with no sandbox backend wired reports none, which is
// the same thing its empty sandbox table means.
func TestSandboxHealthReportsNoSandboxStorageWithoutABackend(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.HostStats = func() (ui.HostPressure, error) {
		return ui.HostPressure{MemoryTotalMB: 1024}, nil
	}

	got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if got.SandboxStorage != nil {
		t.Errorf("sandbox storage = %+v, want none where there is no backend to read", got.SandboxStorage)
	}
}

// errFailedMeasurement is a store that will not answer -- a directory
// that has gone away underneath a daemon that was reading it.
var errFailedMeasurement = errors.New("reading the artifact directory: no such file or directory")
