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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestDBDowngradeAndPrepareUpgradeRefusal(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db := openDataDirStore(t, dataDir)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatal(err)
	}
	futureSchema := model.SchemaVersion + 1
	if _, err := db.Exec("UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", futureSchema); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// 1. `grain state prepare-upgrade` refuses a store newer than this build.
	var prepOut bytes.Buffer
	err := statePrepareUpgrade(ctx, dataDir, &prepOut, nil)
	if !errors.Is(err, model.ErrSchemaTooNew) {
		t.Fatalf("statePrepareUpgrade on newer store = %v, want model.ErrSchemaTooNew", err)
	}
	if _, statErr := os.Stat(daemon.SchemaUpgradeDir(dataDir)); !os.IsNotExist(statErr) {
		t.Fatalf("statePrepareUpgrade wrote %s on a refused downgrade", daemon.SchemaUpgradeDir(dataDir))
	}

	// 2. `grain db downgrade` rejects target >= current schema.
	var badOut bytes.Buffer
	if err := runDBDowngrade(ctx, []string{"-data-dir", dataDir, "-to", fmt.Sprint(futureSchema)}, &badOut); err == nil || !strings.Contains(err.Error(), "must be lower") {
		t.Fatalf("runDBDowngrade(-to %d) = %v, want 'must be lower'", futureSchema, err)
	}

	// 3. `grain db downgrade --to <SchemaVersion>` writes the operator-authored downgrade note.
	var okOut bytes.Buffer
	if err := runDBDowngrade(ctx, []string{"--data-dir", dataDir, "--to", fmt.Sprint(model.SchemaVersion)}, &okOut); err != nil {
		t.Fatalf("runDBDowngrade(--to %d): %v", model.SchemaVersion, err)
	}
	if !strings.Contains(okOut.String(), fmt.Sprintf("prepared operator downgrade %d -> %d", futureSchema, model.SchemaVersion)) {
		t.Errorf("runDBDowngrade output = %q, want prepared summary", okOut.String())
	}
	noteRaw, err := os.ReadFile(filepath.Join(daemon.SchemaUpgradeDir(dataDir), daemon.SchemaUpgradeManifest))
	if err != nil {
		t.Fatalf("reading downgrade manifest: %v", err)
	}
	if !strings.Contains(string(noteRaw), `"operatorDowngrade": true`) {
		t.Errorf("downgrade manifest missing operatorDowngrade=true:\n%s", noteRaw)
	}
}
