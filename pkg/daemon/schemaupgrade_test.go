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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/upgrade"
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

func TestWriteSchemaUpgradeRefusesDowngradeAndRequiresOperatorNoteOnStartup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	// 1. WriteSchemaUpgrade refuses any downgrade (to < from).
	if _, err := WriteSchemaUpgrade(ctx, dataDir, db, "latest", model.SchemaVersion, model.SchemaVersion-1); !errors.Is(err, ErrSchemaDowngradeRefused) {
		t.Fatalf("WriteSchemaUpgrade(downgrade) = %v, want ErrSchemaDowngradeRefused", err)
	}

	// 2. Raising a schema-downgrade Inbox notification records an open notification.
	raiseSchemaDowngradeNotification(store, "latest", model.SchemaVersion, model.SchemaVersion-1,
		errors.New("refusing schema downgrade on channel \"latest\""))
	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true, Family: model.FamilyUpgradeSchemaDowngrade})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(open) != 1 || open[0].Subject != "upgrade.schema-downgrade:latest" {
		t.Fatalf("open schema-downgrade notifications = %+v, want 1 on upgrade.schema-downgrade:latest", open)
	}

	// 3. Without an operator-authored downgrade note, a newer store refuses to start even if a non-operator upgrade.json sits in schema-upgrade/.
	futureSchema := model.SchemaVersion + 1
	if _, err := writeSchemaMigrationNote(ctx, dataDir, db, "latest", futureSchema, model.SchemaVersion, false); err != nil {
		t.Fatalf("writing non-operator note: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", futureSchema); err != nil {
		t.Fatalf("stamping store at futureSchema: %v", err)
	}
	_ = db.Close()

	if _, _, _, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir); !errors.Is(err, model.ErrSchemaTooNew) {
		t.Fatalf("openStoreForSchemaUpgrade with non-operator downgrade note = %v, want model.ErrSchemaTooNew", err)
	}

	// 4. PrepareStoreSchemaUpgrade (setup.sh's helper) also refuses a newer store.
	if _, _, err := PrepareStoreSchemaUpgrade(ctx, dataDir, "scripts/setup.sh"); !errors.Is(err, model.ErrSchemaTooNew) {
		t.Fatalf("PrepareStoreSchemaUpgrade against newer store = %v, want model.ErrSchemaTooNew", err)
	}

	// 5. Explicit PrepareStoreSchemaDowngrade (grain db downgrade -to N) authorizes the rebuild.
	from, dir, err := PrepareStoreSchemaDowngrade(ctx, dataDir, model.SchemaVersion)
	if err != nil || from != futureSchema || dir == "" {
		t.Fatalf("PrepareStoreSchemaDowngrade = (%d, %q, %v), want (%d, non-empty, nil)", from, dir, err, futureSchema)
	}
	rebuilt, rebuiltDB, mig, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStoreForSchemaUpgrade with operator downgrade note: %v", err)
	}
	defer rebuiltDB.Close()
	if !mig.note || mig.from != futureSchema {
		t.Errorf("storeMigration = %+v, want note=true from=%d", mig, futureSchema)
	}
	if sv, err := rebuilt.SchemaVersion(ctx); err != nil || sv != model.SchemaVersion {
		t.Errorf("rebuilt store schema = (%d, %v), want %d", sv, err, model.SchemaVersion)
	}
}

func TestManualUpgradeRefusalThenManualSuccessLeavesNoOpenNotification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer db.Close()

	// Auto-upgrade is off (default config).
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	refFile := filepath.Join(dataDir, "image.env")
	prevDigest := "sha256:" + strings.Repeat("1", 64)
	newDigest := "sha256:" + strings.Repeat("2", 64)
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE=docker.io/grainai/grain:candidate@"+prevDigest+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	schemaFile := filepath.Join(dataDir, "target-schema")
	if err := os.WriteFile(schemaFile, []byte(strconv.Itoa(model.SchemaVersion-1)), 0o600); err != nil {
		t.Fatal(err)
	}
	statusFile := filepath.Join(dataDir, "upgrade-status.json")
	u := upgrade.New(upgrade.Config{
		Image: &upgrade.ImageConfig{
			Repository: "docker.io/grainai/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			DigestCmd:  []string{"sh", "-c", `printf 'docker.io/grainai/grain@%s\n' ` + newDigest, "sh"},
			RunCmd:     []string{"sh", "-c", "cat " + schemaFile, "sh"},
			ListCmd:    []string{"true"},
		},
		// A stub ListTags keeps the upgrader off the real registry: with
		// it nil, Start probes docker.io's own :latest label and refuses
		// up front (or not) on whatever happens to be published there.
		ListTags: func(context.Context, string) ([]string, error) {
			return []string{"latest", "main"}, nil
		},
		HealthCheckArgs: []string{"schema-version"},
		Schema:          upgradeSchemaConfig(Config{DataDir: dataDir}, db, store),
		RestartCmd:      []string{"true"},
		StatusFile:      statusFile,
	})

	// 1. Manual upgrade to "latest" (older schema) fails and raises FamilyUpgradeSchemaDowngrade.
	if err := u.Start("latest"); err != nil {
		t.Fatalf("Start(latest): %v", err)
	}
	<-u.Done()
	st, err := u.Status()
	if err != nil || st.Phase != upgrade.PhaseFailed {
		t.Fatalf("Status after refused manual upgrade = (%+v, %v), want PhaseFailed", st, err)
	}
	open, err := store.ListNotifications(ctx, model.NotificationFilter{
		Open:   true,
		Family: model.FamilyUpgradeSchemaDowngrade,
	})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(open) != 1 || open[0].Subject != "upgrade.schema-downgrade:latest" {
		t.Fatalf("open notifications after manual refusal = %+v, want [upgrade.schema-downgrade:latest]", open)
	}

	// 2. Subsequent manual upgrade to "main" (valid schema) reaches PhaseOK and clears the notification.
	if err := os.WriteFile(schemaFile, []byte(strconv.Itoa(model.SchemaVersion)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start(main): %v", err)
	}
	<-u.Done()
	st, err = u.Status()
	if err != nil || st.Phase != upgrade.PhaseOK {
		t.Fatalf("Status after manual upgrade to main = (%+v, %v), want PhaseOK", st, err)
	}
	open, err = store.ListNotifications(ctx, model.NotificationFilter{
		Open:   true,
		Family: model.FamilyUpgradeSchemaDowngrade,
	})
	if err != nil {
		t.Fatalf("ListNotifications after manual success: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open notifications after manual success = %+v, want none", open)
	}

	// 3. Startup recovery also clears any notification carried across an upgrade restart
	// (e.g. exported during Prepare before PhaseOK), without clearing a newer refusal.
	beforeOK := st.FinishedAt.Add(-time.Second)
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  model.NotificationSubject(model.FamilyUpgradeSchemaDowngrade, "latest"),
		Severity: model.NotificationWarning,
		Title:    "Upgrade to latest refused: schema downgrade",
		Body:     "carried across export",
		RaisedBy: "automation:upgrade",
		RaisedAt: beforeOK,
	}); err != nil {
		t.Fatalf("RaiseNotification (beforeOK): %v", err)
	}
	clearUpgradeSchemaDowngradeAfterRestart(ctx, store, u)
	open, err = store.ListNotifications(ctx, model.NotificationFilter{
		Open:   true,
		Family: model.FamilyUpgradeSchemaDowngrade,
	})
	if err != nil || len(open) != 0 {
		t.Fatalf("after startup clear, open = %+v, err = %v; want none", open, err)
	}

	afterOK := st.FinishedAt.Add(time.Second)
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  model.NotificationSubject(model.FamilyUpgradeSchemaDowngrade, "v3.6.0"),
		Severity: model.NotificationWarning,
		Title:    "Upgrade to v3.6.0 refused: schema downgrade",
		Body:     "raised after last PhaseOK",
		RaisedBy: "automation:upgrade",
		RaisedAt: afterOK,
	}); err != nil {
		t.Fatalf("RaiseNotification (afterOK): %v", err)
	}
	clearUpgradeSchemaDowngradeAfterRestart(ctx, store, u)
	open, err = store.ListNotifications(ctx, model.NotificationFilter{
		Open:   true,
		Family: model.FamilyUpgradeSchemaDowngrade,
	})
	if err != nil || len(open) != 1 {
		t.Fatalf("startup clear should preserve notification raised after PhaseOK, got %+v (%v)", open, err)
	}
}
