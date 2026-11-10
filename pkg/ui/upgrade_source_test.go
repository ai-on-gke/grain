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

func TestUpgradeStatusReportsRegistryDefaultAndUpdatesImageSourceUnlessLocked(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image-ref")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE=docker.io/grainai/grain:main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statusFile := filepath.Join(dir, "upgrade-status.json")
	if err := os.WriteFile(statusFile, []byte(`{
		"branch": "main",
		"phase": "done",
		"source": "registry",
		"detail": "pulled docker.io/grainai/grain:main"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	overrideFile := filepath.Join(dir, "upgrade-image-source.json")

	up := upgrade.New(upgrade.Config{
		StatusFile: statusFile,
		Image: &upgrade.ImageConfig{
			Repository:         "docker.io/grainai/grain",
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
		Source      string `json:"source"`
		ImageSource *struct {
			Source        string `json:"source"`
			SourceDefault bool   `json:"sourceDefault"`
			Locked        bool   `json:"locked"`
			Fallback      string `json:"fallback"`
		} `json:"imageSource"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET /api/upgrade: %v", err)
	}
	if got.Source != "registry" {
		t.Fatalf("GET /api/upgrade source=%q, want registry", got.Source)
	}
	if got.ImageSource == nil || got.ImageSource.Source != "registry" || !got.ImageSource.SourceDefault || got.ImageSource.Locked || got.ImageSource.Fallback != "" {
		t.Fatalf("unexpected default imageSource: %+v", got.ImageSource)
	}

	// Removed release-assets source is rejected with 400 Bad Request.
	rec = do(t, srv, http.MethodPut, "/api/upgrade/source", `{"mode":"release-assets"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "removed") {
		t.Fatalf("PUT /api/upgrade/source(release-assets) = %d: %s", rec.Code, rec.Body.String())
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
			Repository:   "docker.io/grainai/grain",
			RefFile:      refFile,
			Source:       upgrade.ImageSourceRegistry,
			SourceLocked: true,
		},
	})
	lockedSrv := testServerWithUpgrader(t, lockedUp)
	rec = do(t, lockedSrv, http.MethodPut, "/api/upgrade/source", `{"mode":"registry"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("locked PUT /api/upgrade/source = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}
