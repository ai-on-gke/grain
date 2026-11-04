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
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/upgrade"
)

func TestUpgradeStatusReportsAndUpdatesImageSourceUnlessLocked(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image-ref")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statusFile := filepath.Join(dir, "upgrade-status.json")
	if err := os.WriteFile(statusFile, []byte(`{
		"branch": "main",
		"phase": "done",
		"source": "registry",
		"fallbackNote": "release has no image assets; pulled from ghcr.io/bwsalmon/grain/grain",
		"detail": "release has no image assets; pulled from ghcr.io/bwsalmon/grain/grain"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	overrideFile := filepath.Join(dir, "upgrade-image-source.json")

	up := upgrade.New(upgrade.Config{
		StatusFile: statusFile,
		Image: &upgrade.ImageConfig{
			Repository:         "ghcr.io/bwsalmon/grain/grain",
			RefFile:            refFile,
			SourceOverrideFile: overrideFile,
		},
	})
	srv := testServerWithUpgrader(t, up)

	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/upgrade status = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Source       string `json:"source"`
		FallbackNote string `json:"fallbackNote"`
		ImageSource  *struct {
			Source            string `json:"source"`
			SourceDefault     bool   `json:"sourceDefault"`
			Locked            bool   `json:"locked"`
			Fallback          string `json:"fallback"`
			ReleaseRepository string `json:"releaseRepository"`
			ManifestAsset     string `json:"manifestAsset"`
		} `json:"imageSource"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET /api/upgrade: %v", err)
	}
	if got.Source != "registry" || !strings.Contains(got.FallbackNote, "release has no image assets") {
		t.Fatalf("GET /api/upgrade source=%q fallbackNote=%q", got.Source, got.FallbackNote)
	}
	if got.ImageSource == nil || got.ImageSource.Source != "release-assets" || !got.ImageSource.SourceDefault || got.ImageSource.Locked || got.ImageSource.Fallback != "registry" || got.ImageSource.ReleaseRepository != "github.com/ai-on-gke/grain" {
		t.Fatalf("unexpected default imageSource: %+v", got.ImageSource)
	}

	// Update to explicit registry mode via PUT /api/upgrade/source.
	rec = do(t, srv, http.MethodPut, "/api/upgrade/source", `{"mode":"registry"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/upgrade/source status = %d: %s", rec.Code, rec.Body.String())
	}
	got.ImageSource = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode PUT /api/upgrade/source: %v", err)
	}
	if got.ImageSource == nil || got.ImageSource.Source != "registry" || got.ImageSource.SourceDefault || got.ImageSource.Fallback != "" {
		t.Fatalf("unexpected updated imageSource: %+v", got.ImageSource)
	}

	// Locked upgrader refuses PUT /api/upgrade/source with 409 Conflict.
	lockedUp := upgrade.New(upgrade.Config{
		StatusFile: statusFile,
		Image: &upgrade.ImageConfig{
			Repository:   "ghcr.io/bwsalmon/grain/grain",
			RefFile:      refFile,
			Source:       upgrade.ImageSourceReleaseAssets,
			SourceLocked: true,
		},
	})
	lockedSrv := testServerWithUpgrader(t, lockedUp)
	rec = do(t, lockedSrv, http.MethodPut, "/api/upgrade/source", `{"mode":"registry"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("locked PUT /api/upgrade/source = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}
