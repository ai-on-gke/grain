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
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func init() { installtracktest.Register() }

func TestConfigReportsInstallTrack(t *testing.T) {
	t.Parallel()

	t.Run("default test server reports oss track", func(t *testing.T) {
		t.Parallel()
		srv, _ := testServer(t)
		rec := do(t, srv, http.MethodGet, "/api/config", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/config: status = %d: %s", rec.Code, rec.Body)
		}
		got := decode[map[string]any](t, rec)
		if got["installTrack"] != string(installtrack.TrackOSS) {
			t.Errorf("installTrack = %v, want %q", got["installTrack"], installtrack.TrackOSS)
		}
	})

	t.Run("a registered track's host probe reports that track", func(t *testing.T) {
		t.Parallel()
		_, store, _ := testClient(t)
		srv := ui.NewServer(ui.Config{
			Actor:        ui.DefaultActor("alice"),
			Capabilities: ui.OfferedCapabilities(),
			InstallTrackProbe: &installtrack.Probe{
				Hostname: "alice" + installtracktest.HostSuffix,
				AgyPath:  installtracktest.AgyPath,
			},
		}, store)

		rec := do(t, srv, http.MethodGet, "/api/config", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/config: status = %d: %s", rec.Code, rec.Body)
		}
		got := decode[map[string]any](t, rec)
		if got["installTrack"] != string(installtracktest.Track) {
			t.Errorf("installTrack = %v, want %q", got["installTrack"], installtracktest.Track)
		}
	})
}
