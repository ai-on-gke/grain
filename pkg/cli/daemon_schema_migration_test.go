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

// A schema change arriving the way the UI's Upgrade button delivers one:
// nothing moved aside on the host, a store and a dump both written by the
// build that was running a moment ago, and a note beside them saying that
// this build is the one that was upgraded to (grain/task-59).
//
// The other schema-bump test in this directory
// (daemon_schema_bump_test.go) is the same event arriving through
// scripts/setup.sh, and the difference between the two is the whole
// point: there, the rows are already gone by the time the daemon starts
// and all that is left is to say so; here they are still on disk, and a
// deployment that came back without them would have thrown away
// everything an operator was upgrading in order to keep.
//
// Everything runs through run() against a real store and a real bare
// repository, for the reason the other file gives: the failure being
// tested is a process that does not come up, and a unit test of the
// decision would have missed that systemd repeats that failure forever.

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// deploymentMidUpgrade is a host in exactly the state an in-place upgrade
// leaves one in between the cut-over and the restart: a store and a state
// repository both at the previous schema, holding a real deployment's
// rows, and <data-dir>/schema-upgrade holding the export the outgoing
// build made for the incoming one.
//
// The previous schema is faked the only way it can be from inside one
// build -- by restamping what this build wrote, in the store and in the
// dump -- which is enough, because a version stamp is precisely what
// every refusal in the way keys off. What it cannot fake is a dump whose
// *shape* is a version behind, and that is what pkg/staterepo's own
// golden dump is for (migrate_test.go there).
func deploymentMidUpgrade(t *testing.T, prepared bool) (dataDir, remote, taskID string) {
	t.Helper()
	return deploymentMidUpgradeWith(t, prepared, nil)
}

// deploymentMidUpgradeWith is deploymentMidUpgrade with seed run against
// the outgoing build's store just before the upgrade exports it, for the
// rows a test needs that the API does not write.
func deploymentMidUpgradeWith(t *testing.T, prepared bool, seed func(db *sql.DB)) (dataDir, remote, taskID string) {
	t.Helper()
	dataDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
		})
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Filed by the build before the schema change", Repo: "owner/payments-api",
	})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}
	cancel()
	<-runErr

	// The upgrade's own step, run against the database the daemon was
	// just serving: the whole of it, written where the restart cannot
	// take it. This is writeSchemaUpgrade itself rather than a fixture,
	// because what the next start reads has to be what the previous one
	// wrote or this test proves nothing about the pair of them.
	db := openDataDirStore(t, dataDir)
	if seed != nil {
		seed(db)
	}
	if prepared {
		if _, err := daemon.WriteSchemaUpgrade(context.Background(), dataDir, db, "main",
			model.V2BaselineSchemaVersion-1, model.SchemaVersion); err != nil {
			t.Fatalf("writing the schema-upgrade note: %v", err)
		}
	}
	// And the store becomes a pre-2.0 schema's, which is what this
	// build refuses to open rather than migrating in place.
	if _, err := db.Exec("UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", model.V2BaselineSchemaVersion-1); err != nil {
		t.Fatalf("stamping the store with the older schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// The local config dump becomes the previous schema's, as though the
	// preceding build had written it before the upgrade.
	work := daemon.StateRepoDir(dataDir)
	if err := staterepo.WriteSchemaVersion(work, model.V2BaselineSchemaVersion-1); err != nil {
		t.Fatalf("stamping the older schema: %v", err)
	}
	gitIn(t, work, "add", "--all", ".")
	gitIn(t, work, "-c", "user.email=grain@the-build-before", "-c", "user.name=grain",
		"commit", "-m", "Update grain state")

	return dataDir, filepath.Join(work, ".git"), created.ID
}

// openDataDirStore opens the deployment's own database directly, the way
// a second process would -- no Init, because at the point this is used
// the store is stamped with a schema Init refuses.
func openDataDirStore(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return db
}

// The upgrade happened, and the deployment comes back on the same rows:
// the store it could not open is moved aside rather than deleted, a fresh
// one is built at this build's schema out of the rows the upgrade
// exported, and the repository's dump is rewritten from that -- in a
// commit that says the rows came across, since the diff looks exactly
// like the one that says they did not.
func TestAnUpgradeNoteCarriesTheDeploymentsRowsAcrossASchemaChange(t *testing.T) {
	t.Skip("operational SQL is no longer backed up to the state repository")
	dataDir, remote, filed := deploymentMidUpgrade(t, true)

	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		err := daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
		})
		// Into the log as well as into the channel: a start that stops
		// here stops before the UI binds, so the only thing the test
		// would otherwise see is a connection refused with no reason
		// attached to it.
		if err != nil {
			log.Printf("test: the daemon stopped: %v", err)
		}
		runErr <- err
	}()
	defer func() {
		cancel()
		<-runErr
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)

	// The whole point: what the operator filed before the upgrade is
	// still here afterwards.
	task, err := client.Task(ctx, filed)
	if err != nil {
		t.Fatalf("the task filed before the schema change did not survive it: %v", err)
	}
	if task.Title != "Filed by the build before the schema change" {
		t.Errorf("the task came back as %q", task.Title)
	}

	// The store that could not be opened is still on the host, under a
	// name that says what it was: it holds anything the migration could
	// not carry, and it is the only copy of it.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	archived := ""
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "store.schema"+strconv.Itoa(model.V2BaselineSchemaVersion-1)) {
			archived = e.Name()
		}
	}
	if archived == "" {
		t.Errorf("the store at the old schema was not kept; %s holds %v", dataDir, names(entries))
	}

	// The remote has been rewritten at this build's schema, by the start
	// that migrated -- a daemon that had refused to start would never
	// have pushed anything.
	if got := remoteFile(t, remote, staterepo.SchemaVersionFile); got != strconv.Itoa(model.SchemaVersion) {
		t.Fatalf("the remote is stamped %q, want %d", got, model.SchemaVersion)
	}
	if got := remoteFile(t, remote, staterepo.TablesDir+"/task.json"); !strings.Contains(got, filed) {
		t.Errorf("the remote's dump no longer holds the task that was there:\n%s", got)
	}
	if msg := remoteCommitMessage(t, remote); !strings.Contains(msg, "Migrate grain state to schema") {
		t.Errorf("the commit that rewrote the repository says %q, want it to say the rows were migrated "+
			"rather than replaced", msg)
	}

	// And the note is spent. One left lying about would migrate some
	// later restart onto rows that stopped describing this deployment
	// hours ago.
	if _, err := os.Stat(daemon.SchemaUpgradeDir(dataDir)); !os.IsNotExist(err) {
		t.Errorf("%s is still there after being acted on (%v)", daemon.SchemaUpgradeDir(dataDir), err)
	}

	// A deployment, not a rescued database: what is filed now goes out
	// over the repository in the ordinary way.
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Filed after the schema change", Repo: "owner/payments-api",
	}); err != nil {
		t.Fatalf("filing a task after the migration: %v", err)
	}
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}
	if got := remoteFile(t, remote, staterepo.TablesDir+"/task.json"); !strings.Contains(got, "Filed after the schema change") {
		t.Errorf("what was filed after the migration is not in the repository:\n%s", got)
	}
}

// Without the note, a store at another schema stops the process exactly
// as it did before any of this existed. The note is what says these rows
// were read a moment ago by the build that wrote them; a store this build
// cannot open with nothing to explain it is a deployment somebody has to
// look at, not one to rebuild out of whatever dump happens to be lying
// around.
func TestAStoreFromAnotherSchemaStillStopsTheDaemonWhenNothingExplainsIt(t *testing.T) {
	dataDir, _, _ := deploymentMidUpgrade(t, false)
	gapFile := upgrade.GapFile(dataDir)
	gapBefore, err := os.ReadFile(gapFile)
	if err != nil {
		t.Fatalf("the daemon that served left no %s: %v", gapFile, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: freeTCPAddr(t), Actor: "tester",
		})
	}()
	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("the daemon started on a store written at another schema")
		}
		// The schema before this build's is the one before the
		// single-tenant baseline, until a schema change after it, so the
		// refusal is the startup guard's rather than merely "older".
		if !strings.Contains(err.Error(), "older than this build") &&
			!(model.V2BaselineSchemaVersion-1 < model.BaselineSchemaVersion && errors.Is(err, model.ErrBeforeBaseline)) {
			t.Fatalf("the daemon stopped for some other reason: %v", err)
		}
	case <-time.After(daemonWaitLimit):
		t.Fatal("the daemon neither started nor stopped on a store written at another schema")
	}
	// And nothing was moved: a start that refuses has to leave the
	// deployment exactly as it found it, or the operator's own way out
	// (move the store aside, `grain state reformat`) is one step behind
	// the truth.
	if _, err := os.Stat(filepath.Join(dataDir, "store")); err != nil {
		t.Errorf("the store was disturbed by a start that refused to run: %v", err)
	}
	// Including the restart gap: a process that never served has no
	// start time for the build that runs here next to measure from
	// (task 4809). Cancelled first, as the refused process is on its way
	// out, so a shutdown write would have had its chance.
	cancel()
	if gapAfter, err := os.ReadFile(gapFile); err != nil || string(gapAfter) != string(gapBefore) {
		t.Errorf("a start that refused to run rewrote %s:\nbefore %s\nafter  %s (%v)", gapFile, gapBefore, gapAfter, err)
	}
}

// A data dir a build from before the single-tenant reset ran on
// PostgreSQL, started with -postgres-url and so with no db-config.json:
// its state repository is all it holds of the deployment, and its store
// is nowhere this start was told about. Started without the URL, this
// build used to create a fresh SQLite store there and then fail to load
// the dump in staterepo's generic words (task 4815). It is refused in the
// guard's words, and the data dir is left exactly as it was -- store/
// included, by not existing -- so the pre-reset build can still run on it.
func TestAPreResetDataDirWithItsStoreElsewhereIsRefusedUntouched(t *testing.T) {
	dataDir, _, _ := deploymentMidUpgrade(t, false)
	// The store was on PostgreSQL, not here.
	if err := os.RemoveAll(filepath.Join(dataDir, "store")); err != nil {
		t.Fatal(err)
	}
	work := daemon.StateRepoDir(dataDir)
	preReset := model.BaselineSchemaVersion - 2
	if err := staterepo.WriteSchemaVersion(work, preReset); err != nil {
		t.Fatalf("stamping the pre-reset schema: %v", err)
	}
	gitIn(t, work, "add", "--all", ".")
	gitIn(t, work, "-c", "user.email=grain@the-build-before", "-c", "user.name=grain",
		"commit", "-m", "Update grain state")
	seedTestAccessCode(t, dataDir)
	before := treeSnapshot(t, dataDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: freeTCPAddr(t), Actor: "tester",
		})
	}()
	select {
	case err := <-runErr:
		if !errors.Is(err, model.ErrBeforeBaseline) {
			t.Fatalf("got %v, want the startup guard's refusal of a pre-reset data dir", err)
		}
		for _, want := range []string{"single-tenant only", "this data dir was written by a build from before the single-tenant reset",
			"schema " + strconv.Itoa(preReset), "It needs a fresh data dir", "Nothing in this data dir has been changed"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q: %v", want, err)
			}
		}
	case <-time.After(daemonWaitLimit):
		t.Fatal("the daemon neither started nor stopped on a pre-reset data dir")
	}
	// Cancelled first, as for restart-gap.json above (task 4809): the
	// refused process is on its way out, and a shutdown write would have
	// had its chance.
	cancel()
	after := treeSnapshot(t, dataDir)
	for path, was := range before {
		if now, ok := after[path]; !ok {
			t.Errorf("a start that refused to run removed %s", path)
		} else if now != was {
			t.Errorf("a start that refused to run changed %s", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("a start that refused to run created %s", path)
		}
	}
}

// treeSnapshot is everything under dir, as `diff -r` would compare it:
// each path, relative to dir, against its type and contents.
func treeSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			out[rel] = "dir"
		case d.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "symlink " + target
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[rel] = "file " + string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	return out
}

// The upgrade happened, onto this single-tenant build, from a deployment
// with teams (docs/single-tenant-reset.md, stage 5). The rehearsal before
// the cut-over is what stops that upgrade (staterepo.CheckMigration); this
// is the start after an upgrade that skipped it, which has to refuse the
// rows it would otherwise import and serve -- and keep them, in the store
// it came from, rather than drop them.
func TestAnUpgradeFromAMultiTenantDeploymentDoesNotStart(t *testing.T) {
	dataDir, _, _ := deploymentMidUpgradeWith(t, true, func(db *sql.DB) {
		// The team table as the build before schema 24 had it, holding
		// the default team and one besides it: this build has no such
		// table, so its export carries the rows for the dump check
		// (staterepo's retired.go) to refuse.
		for _, q := range []string{
			"CREATE TABLE `team` (`id` TEXT NOT NULL PRIMARY KEY, `name` TEXT NOT NULL)",
			"INSERT INTO `team` (`id`, `name`) VALUES ('default', 'admin'), ('beta', 'beta')",
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: freeTCPAddr(t), Actor: "tester",
		})
	}()
	select {
	case err := <-runErr:
		want := model.ErrMultiTenantData
		if model.V2BaselineSchemaVersion-1 < model.BaselineSchemaVersion {
			want = model.ErrBeforeBaseline
		}
		if !errors.Is(err, want) {
			t.Fatalf("got %v, want the daemon to refuse a multi-tenant deployment's rows (%v)", err, want)
		}
		// The rows are counted only once the export's schema is one this
		// build reads. The schema before this build's is from before the
		// single-tenant baseline until a schema change after it, and the
		// guard refuses that version first, before the store moves.
		wants := []string{"single-tenant only", "teams besides the default", "grain connections import"}
		if model.V2BaselineSchemaVersion-1 < model.BaselineSchemaVersion {
			wants = []string{"single-tenant only", "before the single-tenant baseline", "grain connections import"}
		}
		for _, want := range wants {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q: %v", want, err)
			}
		}
	case <-time.After(daemonWaitLimit):
		t.Fatal("the daemon neither started nor stopped on a multi-tenant deployment's rows")
	}
	// The store the upgrade came from is still there, moved aside with
	// its rows, and so is the export.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var archived string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "store.schema"+strconv.Itoa(model.V2BaselineSchemaVersion-1)+"-") {
			archived = filepath.Join(dataDir, e.Name())
		}
	}
	if archived == "" {
		// Refused before the move: Init's guard counts the rows of the
		// store the upgrade came from before it reads that store's
		// version, so the store is where it was, rows and all.
		archived = filepath.Join(dataDir, "store")
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(archived))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var teams int
	if err := db.QueryRow("SELECT COUNT(*) FROM `team` WHERE `id` = 'beta'").Scan(&teams); err != nil {
		t.Fatal(err)
	}
	if teams != 1 {
		t.Errorf("the store moved aside holds %d beta teams, want the 1 it was written with", teams)
	}
	if _, err := os.Stat(daemon.SchemaUpgradeDir(dataDir)); err != nil {
		t.Errorf("the upgrade's export is gone: %v", err)
	}
}

// The migration interrupted between its two halves: the store rebuilt at
// this build's schema, the state repository not yet brought across, and
// the process gone before it could be. The note is what makes that
// recoverable -- it is removed only once both halves are done -- and
// without this the next start would meet a dump it cannot read with
// nothing authorising a rewrite, which is the same restart loop one step
// further along.
func TestAnInterruptedMigrationIsFinishedByTheNextStart(t *testing.T) {
	dataDir, remote, filed := deploymentMidUpgrade(t, true)
	// What the first start would have left behind: a store this build
	// opens happily, and a repository still at the old schema.
	db := openDataDirStore(t, dataDir)
	if _, err := db.Exec("UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", model.SchemaVersion); err != nil {
		t.Fatalf("stamping the store with this build's schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		err := daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
		})
		if err != nil {
			log.Printf("test: the daemon stopped: %v", err)
		}
		runErr <- err
	}()
	defer func() {
		cancel()
		<-runErr
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)

	if _, err := client.Task(ctx, filed); err != nil {
		t.Fatalf("the task this deployment was already running on is gone: %v", err)
	}
	if got := remoteFile(t, remote, staterepo.SchemaVersionFile); got != strconv.Itoa(model.SchemaVersion) {
		t.Errorf("the remote is stamped %q, want %d -- the half that was owed did not happen",
			got, model.SchemaVersion)
	}
	if _, err := os.Stat(daemon.SchemaUpgradeDir(dataDir)); !os.IsNotExist(err) {
		t.Errorf("%s is still there after both halves finished (%v)", daemon.SchemaUpgradeDir(dataDir), err)
	}
}

// A note that names some other build is not consent for this one. It is
// what a deployment that upgraded, rolled back, and later restarted has
// lying in its data directory, and acting on it would import rows shaped
// for a schema this build does not have.
func TestAnUpgradeNoteForAnotherBuildIsNotActedOn(t *testing.T) {
	dataDir, _, _ := deploymentMidUpgrade(t, true)
	note := filepath.Join(daemon.SchemaUpgradeDir(dataDir), daemon.SchemaUpgradeManifest)
	data, err := os.ReadFile(note)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.Replace(string(data), `"to": `+strconv.Itoa(model.SchemaVersion),
		`"to": `+strconv.Itoa(model.SchemaVersion+3), 1)
	if rewritten == string(data) {
		t.Fatalf("the note does not name the schema it was written for:\n%s", data)
	}
	if err := os.WriteFile(note, []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: freeTCPAddr(t), Actor: "tester",
		})
	}()
	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("the daemon migrated itself on a note written for a different build")
		}
	case <-time.After(daemonWaitLimit):
		t.Fatal("the daemon neither started nor stopped")
	}
}

func names(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func remoteFile(t *testing.T, remote, path string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", remote, "show", "main:"+path).CombinedOutput()
	if err != nil {
		t.Fatalf("reading %s from the remote: %v: %s", path, err, out)
	}
	return strings.TrimSpace(string(out))
}

func remoteCommitMessage(t *testing.T, remote string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", remote, "log", "-1", "--format=%B", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the remote's last commit: %v: %s", err, out)
	}
	return string(out)
}
