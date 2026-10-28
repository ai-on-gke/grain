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

// `grain state check` end to end: a directory of state files in, a
// verdict out, with no data directory, no daemon and no store of its own
// -- which is the whole shape of the CI step this exists to be.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// exportedState writes a real dump of a real database, the way a running
// daemon's sync does, and returns the directory holding it.
func exportedState(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer db.Close()
	if err := store.PutTask(ctx, model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "Rename the endpoint",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
			Reason:      model.ReasonDirect,
		},
	}); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatalf("stamping: %v", err)
	}
	return dir
}

func TestStateCheckAcceptsAnExportedRepository(t *testing.T) {
	t.Parallel()
	if err := runState([]string{"check", exportedState(t)}); err != nil {
		t.Fatalf("checking a dump grain wrote: %v", err)
	}
}

// -data-dir is required by every other `grain state` command and must
// not be by this one: the runner checking a pull request has a checkout
// and nothing else.
func TestStateCheckNeedsNoDataDir(t *testing.T) {
	dir := exportedState(t)
	t.Chdir(dir)
	// No argument at all, which is what `grain state check` in a CI step
	// at the top of the checkout looks like.
	if err := runState([]string{"check"}); err != nil {
		t.Fatalf("checking the working directory: %v", err)
	}
}

func TestStateCheckFailsOnADumpThatWillNotLoad(t *testing.T) {
	t.Parallel()
	dir := exportedState(t)
	path := filepath.Join(dir, staterepo.TablesDir, "task.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	// A column the schema insists on, gone -- the hand edit that used to
	// take a deployment down at its next start.
	delete(rows[0], "title")
	edited, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(edited, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	err = runState([]string{"check", dir})
	if err == nil {
		t.Fatal("a task row with no title passed `grain state check`")
	}
	if !strings.Contains(err.Error(), "task") {
		t.Errorf("error does not name what broke: %v", err)
	}
}

func TestStateCheckRefusesADirectoryThatIsNotAStateRepository(t *testing.T) {
	t.Parallel()
	if err := runState([]string{"check", t.TempDir()}); err == nil {
		t.Fatal("an empty directory passed `grain state check`")
	}
}

// `grain state check -team` checked a team's grain/config-<team>
// repository. There is only grain/config now, so the flag is gone and
// asking for it is a usage error rather than a check of something else.
func TestStateCheckHasNoTeamMode(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"check", "-team", "alpha", t.TempDir()},
		{"check", "-taken", "template=x", t.TempDir()},
	} {
		if err := runState(args); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("runState(%q) = %v, want an undefined-flag error", args, err)
		}
	}
}
