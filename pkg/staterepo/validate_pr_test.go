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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// liveDeployment is a running deployment's database: one connection
// serving the default model, two runs at a time.
func liveDeployment(t *testing.T) (*sql.DB, *model.Store, model.Config) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultConfig()
	cfg.MaxWorkers = 2
	cfg.AgentBackends = []model.AgentBackend{{ID: "c", Framework: "claude", Models: []string{"claude-opus-5-5"}}}
	cfg.DefaultModel = "claude-opus-5-5"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	return db, store, cfg
}

// candidate is the tree a config pull request would leave: the live
// configuration with edit made to it, exported. The live row is put
// back afterwards.
func candidate(t *testing.T, db *sql.DB, store *model.Store, live model.Config, edit func(*model.Config)) string {
	t.Helper()
	ctx := context.Background()
	cfg := live
	cfg.AgentBackends = append([]model.AgentBackend(nil), live.AgentBackends...)
	edit(&cfg)
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConfig(ctx, live); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidateCandidateConfigPassesAChangeThatKeepsDispatchRunning(t *testing.T) {
	t.Parallel()
	db, store, live := liveDeployment(t)
	dir := candidate(t, db, store, live, func(c *model.Config) { c.PromptExtension = "be kind" })
	report, err := staterepo.ValidateCandidateConfig(context.Background(), db, dir)
	if err != nil {
		t.Fatalf("ValidateCandidateConfig: %v (%+v)", err, report)
	}
	if report.Check.Rows["grain_config"] != 1 || len(report.Liveness) != 0 {
		t.Fatalf("report = %+v", report)
	}
	// The live database is untouched by the rehearsal.
	got, err := store.GetConfig(context.Background())
	if err != nil || got.PromptExtension != "" {
		t.Fatalf("the live row changed: %+v %v", got, err)
	}
}

func TestValidateCandidateConfigRefusesAChangeThatLocksDispatch(t *testing.T) {
	t.Parallel()
	db, store, live := liveDeployment(t)
	for _, tc := range []struct {
		name string
		edit func(*model.Config)
		want string
	}{
		{"no runs", func(c *model.Config) { c.MaxWorkers = 0 }, "max_parallel is 0"},
		{"every connection off", func(c *model.Config) { c.AgentBackends[0].Disabled = true }, "every model connection is disabled"},
		{"default model unserved", func(c *model.Config) { c.DefaultModel = "gpt-9" }, "default_model gpt-9 is served by no enabled connection"},
		{"director gone", func(c *model.Config) { c.ConsoleSessions = []model.ConsoleSession{} }, `no longer has the "director" session`},
		{"director model unserved", func(c *model.Config) {
			c.ConsoleSessions = []model.ConsoleSession{{ID: model.DirectorSessionID, Title: "Director", Model: "gpt-9"}}
		}, "director_model gpt-9"},
	} {
		dir := candidate(t, db, store, live, tc.edit)
		report, err := staterepo.ValidateCandidateConfig(context.Background(), db, dir)
		if !errors.Is(err, staterepo.ErrDispatchLocked) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, report %+v; want ErrDispatchLocked naming %q", tc.name, err, report, tc.want)
		}
	}
}

func TestValidateCandidateConfigRefusesATreeThatDoesNotLoad(t *testing.T) {
	t.Parallel()
	db, store, live := liveDeployment(t)
	dir := candidate(t, db, store, live, func(*model.Config) {})
	if err := os.WriteFile(filepath.Join(dir, staterepo.TablesDir, "grain_config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := staterepo.ValidateCandidateConfig(context.Background(), db, dir); err == nil {
		t.Fatal("a tree whose settings file is not JSON passed validation")
	}
}

func TestRehearseApplyNeverWritesTheLiveDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, store, live := liveDeployment(t)
	dir := candidate(t, db, store, live, func(c *model.Config) { c.MaxWorkers = 7 })
	if err := staterepo.RehearseApply(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got.MaxWorkers != 2 {
		t.Fatalf("the rehearsal wrote the live row: %+v %v", got, err)
	}
}
