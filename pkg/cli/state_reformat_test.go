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
	"os"
	"path/filepath"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
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

	if err := stateReformat(dataDir); err != nil {
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
