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

package daemon

// What an export that fails part-way leaves under the data directory
// (grain/task-255).
//
// Nothing is left armed by such a failure -- writeSchemaUpgrade writes
// the manifest last, and readSchemaUpgrade acts only on a note it can
// read -- but a whole copy of a deployment's rows sitting in the data
// directory for nothing is its own problem, and the only thing that can
// say whether removing that directory is safe is the call that was in
// the middle of writing it. Hence the two halves below: past the opening
// RemoveAll everything under there is this call's own work and goes with
// the failure, and if that RemoveAll is itself what failed then whatever
// survived it belongs to an earlier upgrade and is left where it is
// rather than attacked a second time under another name.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestAFailedSchemaExportTakesItsHalfWrittenDumpWithIt(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	_, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	// A database the export cannot read: the shape of a host that has run
	// out of disk, or of a store that went away mid-upgrade. What matters
	// is that Prepare gets as far as making the directory and then fails.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteSchemaUpgrade(context.Background(), dataDir, db, "main",
		model.SchemaVersion, model.SchemaVersion+1); err == nil {
		t.Fatal("writeSchemaUpgrade succeeded against a closed database, so this test proves nothing")
	}

	if _, err := os.Stat(SchemaUpgradeDir(dataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s is still here after a failed export (stat: %v) -- a partial copy of this deployment's "+
			"rows would sit there until some later upgrade happened to clear it",
			SchemaUpgradeDir(dataDir), err)
	}
}

func TestAnExportThatCannotClearTheGroundLeavesTheEarlierNoteAlone(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("running as root, so an unreadable directory does not stop the export clearing it")
	}
	dataDir := t.TempDir()
	_, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer db.Close()

	// An earlier upgrade's note, still valid and still waiting for the
	// restart that spends it.
	dir := SchemaUpgradeDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const note = `{"from":1,"to":2,"writtenAt":"2026-01-01T00:00:00Z"}`
	manifest := filepath.Join(dir, SchemaUpgradeManifest)
	if err := os.WriteFile(manifest, []byte(note+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And a directory the export cannot get into to clear it: os.RemoveAll
	// cannot so much as enumerate this, so it fails having removed
	// nothing at all.
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if _, err := WriteSchemaUpgrade(context.Background(), dataDir, db, "main",
		model.SchemaVersion, model.SchemaVersion+1); err == nil {
		t.Fatal("writeSchemaUpgrade succeeded with a schema-upgrade directory it cannot read")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("the earlier upgrade's note is gone (%v) -- it was still pending a restart, and this "+
			"failure never touched it", err)
	}
	if string(got) != note+"\n" {
		t.Errorf("the earlier upgrade's note = %q, want it left at %q", got, note)
	}
}
