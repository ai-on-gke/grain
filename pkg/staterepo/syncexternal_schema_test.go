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

package staterepo_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// TestSyncExternalRefusesANewerBuildsDump pins
// docs/test-runs/2026-11-15-config-grants-export.md: a deployment reading
// an external grain/config in read-only IaC mode refused a newer build's
// dump at startup, but its sync tick (and `grain state sync`) imported
// one pushed afterwards without a word. An older build reads a newer
// build's grants: repo#read/#write entries as opaque capabilities, so on
// v3.1.1 that import dropped every template's and schedule's plain reads
// and writes. SyncExternal now refuses the dump as Apply does, imports
// nothing, and goes on refusing it on every tick.
func TestSyncExternalRefusesANewerBuildsDump(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	work := filepath.Join(t.TempDir(), "work")
	git(t, filepath.Dir(work), "clone", "--quiet", remote, work)
	git(t, work, "checkout", "--quiet", "-B", "main")
	publish := func(db *sql.DB, version int, message string) {
		t.Helper()
		if err := staterepo.Export(ctx, db, work); err != nil {
			t.Fatalf("exporting: %v", err)
		}
		if err := os.WriteFile(filepath.Join(work, staterepo.SchemaVersionFile), []byte(strconv.Itoa(version)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, work, "add", "--all", ".")
		git(t, work, "-c", "user.email=writer@example.com", "-c", "user.name=writer", "commit", "--quiet", "-m", message)
		git(t, work, "push", "--quiet", "origin", "HEAD:main")
	}
	docs := model.RepoRef{Owner: "acme", Name: "docs"}
	template := func(reads ...model.RepoRef) model.Template {
		return model.Template{ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", Reads: reads, CreatedAt: now}
	}

	// The shared repository as this build writes it.
	writer, writerDB := openDB(t)
	if err := writer.PutTemplate(ctx, template(docs)); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	publish(writerDB, model.SchemaVersion, "this build's export")

	reader, db := openDB(t)
	r, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if _, err := staterepo.SyncExternal(ctx, r, db, true); err != nil {
		t.Fatalf("syncing this build's own export: %v", err)
	}
	reads := func() []model.RepoRef {
		t.Helper()
		got, err := reader.GetTemplate(ctx, "tpl-1")
		if err != nil || got == nil {
			t.Fatalf("reading tpl-1 back: %v", err)
		}
		return got.Reads
	}
	if got := reads(); len(got) != 1 || got[0] != docs {
		t.Fatalf("tpl-1 reads after this build's export = %v, want [%s]", got, docs)
	}

	// A newer build's export lands on top: a schema this build does not
	// know, and rows this build would read differently (here, no read).
	newer, newerDB := openDB(t)
	if err := newer.PutTemplate(ctx, template()); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	publish(newerDB, model.SchemaVersion+1, "a newer build's export")

	for _, force := range []bool{false, true} {
		applied, err := staterepo.SyncExternal(ctx, r, db, force)
		if !errors.Is(err, staterepo.ErrSchemaTooNew) || !errors.Is(err, staterepo.ErrNotApplied) {
			t.Fatalf("SyncExternal(force=%v) of a newer build's dump = %v, want ErrSchemaTooNew wrapped in ErrNotApplied", force, err)
		}
		if applied {
			t.Errorf("SyncExternal(force=%v) of a newer build's dump reported it applied", force)
		}
		if got := reads(); len(got) != 1 || got[0] != docs {
			t.Fatalf("tpl-1 reads after refusing a newer build's dump (force=%v) = %v, want [%s] kept", force, got, docs)
		}
	}
}
