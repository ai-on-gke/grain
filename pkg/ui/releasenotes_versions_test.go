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

// The per-version release notes the binary carries, as the Metrics pane
// reads them, and the upgrade pane's preview of what a target brings.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/releasenotes"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

type releaseVersionsResponse struct {
	Running  string                 `json:"running"`
	Versions []releasenotes.Version `json:"versions"`
}

// Metrics -> Release notes lists v1.8.0 and earlier out of the running
// binary, with no repository to ask.
func TestReleaseVersionsAreListedFromTheBinary(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/release-notes/versions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET versions = %d: %s", rec.Code, rec.Body)
	}
	got := decode[releaseVersionsResponse](t, rec)
	var names []string
	for _, v := range got.Versions {
		names = append(names, v.Version)
	}
	if len(names) == 0 || !strings.Contains(strings.Join(names, " "), "v1.8.0") {
		t.Fatalf("versions = %v, want v1.8.0 among them", names)
	}
	for i := 1; i < len(names); i++ {
		if releasenotes.Compare(names[i-1], names[i]) <= 0 {
			t.Errorf("versions are not newest first: %v", names)
		}
	}

	rec = do(t, srv, http.MethodGet, "/api/release-notes/versions/v1.8.0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET v1.8.0 = %d: %s", rec.Code, rec.Body)
	}
	note := decode[releasenotes.Note](t, rec)
	if note.Version.Version != "v1.8.0" || note.Date != "2026-10-11" || !strings.HasPrefix(note.Markdown, "# v1.8.0") {
		t.Errorf("v1.8.0 = %+v", note.Version)
	}

	if rec := do(t, srv, http.MethodGet, "/api/release-notes/versions/v0.0.1", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a version with no notes = %d, want 404", rec.Code)
	}
}

// The weekly routes beside them still answer as they did: "versions" is
// not read as a week, and a week is not read as a version.
func TestReleaseVersionsDoNotShadowTheWeeklyNotes(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/api/release-notes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET weekly list = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "v1.8.0") {
		t.Errorf("the weekly list carries per-version notes: %s", rec.Body)
	}
	rec = do(t, srv, http.MethodGet, "/api/release-notes/2026-W38", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"period":"2026-W38"`) {
		t.Errorf("GET a week = %d: %s", rec.Code, rec.Body)
	}
}

// Choosing an upgrade target asks that target's image what it brings.
func TestUpgradeReleaseNotesPreviewsTheTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	answer := filepath.Join(dir, "answer.json")
	if err := os.WriteFile(answer, []byte(`{"version":"v1.8.0","notes":[{"version":"v1.8.0","title":"v1.8.0","markdown":"# v1.8.0\n"},{"version":"v1.7.0","title":"v1.7.0","markdown":"# v1.7.0\n"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	up := upgrade.New(upgrade.Config{
		Image: &upgrade.ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			PullCmd:    []string{"true"},
			DigestCmd:  []string{"sh", "-c", `printf '%s@%s\n' "${1%:*}" ` + testImageDigest, "sh"},
			RunCmd:     []string{"sh", "-c", "cat " + answer, "sh"},
		},
		ReleaseNotesArgs: []string{"release-notes", "--json"},
		RunningRelease:   func() string { return "v1.7.0" },
		StatusFile:       filepath.Join(dir, "upgrade-status.json"),
		AllowedBranches:  []string{"v*"},
	})
	srv := testServerWithUpgrader(t, up)

	rec := do(t, srv, http.MethodGet, "/api/upgrade/release-notes?branch=v1.8.0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body)
	}
	got := decode[upgrade.ReleaseNotesPreview](t, rec)
	if len(got.Notes) != 1 || got.Notes[0].Version.Version != "v1.8.0" || got.NotInThisBuild {
		t.Errorf("preview = %+v, want v1.8.0's notes alone", got)
	}

	if rec := do(t, srv, http.MethodGet, "/api/upgrade/release-notes?branch=main", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("a branch this deployment would not upgrade onto = %d, want 400", rec.Code)
	}
	if rec := do(t, testServerWithUpgrader(t, nil), http.MethodGet, "/api/upgrade/release-notes?branch=v1.8.0", ""); rec.Code != http.StatusNotFound {
		t.Errorf("without an upgrader = %d, want 404", rec.Code)
	}
}

// An upgrader that cannot preview (an older double) answers "not in
// this build" rather than failing the pane.
func TestUpgradeReleaseNotesWithoutAPreviewingUpgrader(t *testing.T) {
	t.Parallel()
	srv := testServerWithUpgrader(t, brokenUpgrader{})
	rec := do(t, srv, http.MethodGet, "/api/upgrade/release-notes?branch=main", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body)
	}
	if got := decode[upgrade.ReleaseNotesPreview](t, rec); !got.NotInThisBuild || got.Message != upgrade.NotInThisBuild {
		t.Errorf("preview = %+v", got)
	}
}
