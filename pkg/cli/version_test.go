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

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
	"github.com/ai-on-gke/grain/pkg/version"
)

func TestFormatVersion(t *testing.T) {
	tm := time.Date(2026, 9, 3, 14, 2, 11, 0, time.UTC)

	cases := []struct {
		name string
		info version.Info
		want string
	}{
		{
			name: "unstamped",
			info: version.Info{},
			want: "dev (unstamped)",
		},
		{
			name: "full stamp without release",
			info: version.Info{
				Revision: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
				Time:     tm,
			},
			want: "0fbfb46 · 2026-09-03 14:02Z",
		},
		{
			name: "full stamp with release and modified",
			info: version.Info{
				Release:  "v1.8.0",
				Revision: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
				Time:     tm,
				Modified: true,
			},
			want: "v1.8.0 · 0fbfb46-dirty · 2026-09-03 14:02Z",
		},
		{
			name: "release alone",
			info: version.Info{
				Release: "v1.8.0",
			},
			want: "v1.8.0",
		},
		{
			name: "unstamped google edition",
			info: version.Info{
				Edition: "google",
			},
			want: "dev (google, unstamped)",
		},
		{
			name: "google edition with release and revision",
			info: version.Info{
				Release:  "v2.0.0-google",
				Edition:  "google",
				Revision: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
				Time:     tm,
			},
			want: "v2.0.0-google · google · 0fbfb46 · 2026-09-03 14:02Z",
		},
		{
			// A main build's version is printed whole, its +<sha7> build
			// metadata included.
			name: "main build version",
			info: version.Info{
				Release:  "v2.3.0-dev.114+0fbfb46",
				Revision: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
				Time:     tm,
			},
			want: "v2.3.0-dev.114+0fbfb46 · 0fbfb46 · 2026-09-03 14:02Z",
		},
		{
			name: "unversioned build with Go's pseudo-version",
			info: version.Info{
				Release:     "v1.14.0-rc.1.0.20260903140211-0fbfb4619f0a",
				Revision:    "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
				Time:        tm,
				Unversioned: true,
			},
			want: "v1.14.0-rc.1.0.20260903140211-0fbfb4619f0a (unversioned) · 0fbfb46 · 2026-09-03 14:02Z",
		},
		{
			name: "unversioned build with no release at all",
			info: version.Info{
				Revision:    "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
				Unversioned: true,
			},
			want: "unversioned · 0fbfb46",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatVersion(tc.info)
			if got != tc.want {
				t.Errorf("formatVersion(%+v) = %q, want %q", tc.info, got, tc.want)
			}
		})
	}
}

func TestVersionCmd_Output(t *testing.T) {
	out := captureStdout(t, func() {
		versionCmd([]string{})
	})
	got := strings.TrimSpace(out)
	if got == "" {
		t.Error("expected non-empty output from versionCmd")
	}

	// -json output
	jsonOut := captureStdout(t, func() {
		versionCmd([]string{"-json"})
	})
	var parsed versionOutput
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("unmarshaling versionCmd -json output: %v", err)
	}
	if !slices.Contains(installtrack.Tracks(), installtrack.Track(parsed.InstallTrack)) {
		t.Errorf("parsed.InstallTrack = %q, want one of %v", parsed.InstallTrack, installtrack.Tracks())
	}
}

func TestVersionCmd_InstallTrackClassification(t *testing.T) {
	installtracktest.Register()
	// Hermetic OSS probe
	ossJSON := captureStdout(t, func() {
		versionCmdWithProbe([]string{"-json"}, installtrack.Probe{Hostname: "dev.example.com"})
	})
	var ossOut versionOutput
	if err := json.Unmarshal([]byte(ossJSON), &ossOut); err != nil {
		t.Fatalf("unmarshaling oss versionCmd -json: %v", err)
	}
	if ossOut.InstallTrack != string(installtrack.TrackOSS) {
		t.Errorf("ossOut.InstallTrack = %q, want %q", ossOut.InstallTrack, installtrack.TrackOSS)
	}

	ossTrack := strings.TrimSpace(captureStdout(t, func() {
		versionCmdWithProbe([]string{"-track"}, installtrack.Probe{Hostname: "dev.example.com"})
	}))
	if ossTrack != string(installtrack.TrackOSS) {
		t.Errorf("version -track (oss) = %q, want %q", ossTrack, installtrack.TrackOSS)
	}

	// Unit file with a registered track's flags
	dir := t.TempDir()
	unitFile := filepath.Join(dir, "grain-daemon.service")
	if err := os.WriteFile(unitFile, []byte("ExecStart=/usr/local/bin/grain daemon -agy-path "+installtracktest.AgyPath+" -ui-allowed-hosts grain.example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackJSON := captureStdout(t, func() {
		versionCmdWithProbe([]string{"-json", "-unit-file", unitFile}, installtrack.Probe{Hostname: "dev.example.com"})
	})
	var trackOut versionOutput
	if err := json.Unmarshal([]byte(trackJSON), &trackOut); err != nil {
		t.Fatalf("unmarshaling the registered track's versionCmd -json: %v", err)
	}
	if trackOut.InstallTrack != string(installtracktest.Track) {
		t.Errorf("trackOut.InstallTrack = %q, want %q", trackOut.InstallTrack, installtracktest.Track)
	}
	if !slices.Contains(trackOut.InstallTrackSignals, installtracktest.SignalAgyPath) {
		t.Errorf("trackOut.InstallTrackSignals = %v, want %q", trackOut.InstallTrackSignals, installtracktest.SignalAgyPath)
	}

	trackName := strings.TrimSpace(captureStdout(t, func() {
		versionCmdWithProbe([]string{"-track", "-unit-file", unitFile}, installtrack.Probe{Hostname: "dev.example.com"})
	}))
	if trackName != string(installtracktest.Track) {
		t.Errorf("version -track (acme) = %q, want %q", trackName, installtracktest.Track)
	}
}
