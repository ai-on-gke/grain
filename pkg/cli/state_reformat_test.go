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

// `grain state reformat` is the operator's copy of the note
// scripts/setup.sh leaves on a schema bump: it says the schema changed
// under this deployment, so the next start replaces a dump it cannot
// read instead of refusing to run on it.
//
// It exists for the deployments setup.sh never runs on -- the UI's
// in-place Upgrade button swaps the image and nothing moves a store
// aside -- so what it must not need is the two things such a host cannot
// offer: a store this build can open, and a reachable remote.

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// dumpAt writes just enough of a working tree to be a dump at a given
// schema: what a build that is no longer running left behind.
func dumpAt(t *testing.T, dataDir string, version int) {
	t.Helper()
	dir := daemon.StateRepoDir(dataDir)
	if err := os.MkdirAll(filepath.Join(dir, staterepo.TablesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, staterepo.TablesDir, "task.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, version); err != nil {
		t.Fatal(err)
	}
}

func TestStateReformatWritesTheNoteTheNextStartReads(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	dumpAt(t, dataDir, model.SchemaVersion-1)

	var out bytes.Buffer
	if err := stateReformatTo(dataDir, &out); err != nil {
		t.Fatalf("reformat: %v", err)
	}
	stamp := daemon.StateRepoReformatStamp(dataDir)
	body, err := os.ReadFile(stamp)
	if err != nil {
		t.Fatalf("the note the daemon reads was not written: %v", err)
	}
	if len(body) == 0 {
		t.Fatalf("%s is empty; whoever finds it has nothing to go on", stamp)
	}
	printed := out.String()
	wantBaseline := "schema " + strconv.Itoa(model.V2BaselineSchemaVersion) + " or newer migrates in place"
	if !strings.Contains(printed, wantBaseline) {
		t.Errorf("reformat output = %q, want it to say a store at %s", printed, wantBaseline)
	}
	if strings.Contains(printed, "If the store is at the old schema too") {
		t.Errorf("reformat output still gives the pre-2.0 move-aside advice:\n%s", printed)
	}
}

// A repository already at this build's schema needs nothing, and a note
// written where nothing needed it would sit in the data directory until
// some later schema change found it and took it for consent.
func TestStateReformatWritesNothingWhenNothingHasChanged(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	dumpAt(t, dataDir, model.SchemaVersion)

	if err := stateReformat(dataDir); err != nil {
		t.Fatalf("reformat: %v", err)
	}
	if _, err := os.Stat(daemon.StateRepoReformatStamp(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("a repository at this build's own schema was authorised for a reformat (%v)", err)
	}
}

// A host whose working tree was archived by the schema bump has no dump
// here to compare against, and it is the case that most needs the note:
// the dump that matters is on the remote, and this host has not cloned
// it yet.
func TestStateReformatWritesTheNoteWithNoWorkingTreeAtAll(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	if err := stateReformat(dataDir); err != nil {
		t.Fatalf("reformat: %v", err)
	}
	if _, err := os.Stat(daemon.StateRepoReformatStamp(dataDir)); err != nil {
		t.Fatalf("the note was not written for a host with no working tree: %v", err)
	}
}
