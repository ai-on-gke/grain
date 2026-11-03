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

package statecheck

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// grain/task-2649: a deployment holding a schedule fired by a GitHub
// workflow run exported it as `trigger: {github_workflow_run: ...}`, and
// the import never looked for `trigger`, so the schema check an upgrade
// runs (`grain state check -migrate`) refused the deployment's own dump
// with "NOT NULL constraint failed: schedule.recurrence_kind" -- and the
// upgrade with it. This is that check, against that dump.
func TestAMigrationCheckLoadsAScheduleFiredByAWorkflowRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()
	store := model.New(db)
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatalf("putting default config: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "repo:schedule:example:repo:nightly-ci-review", Title: "Review a failed nightly",
		Target: model.RepoRef{Owner: "example", Name: "repo"},
		Recurrence: model.Recurrence{
			Kind: model.RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
				Workflow:    "nightly.yml",
				Conclusions: []string{"failure", "timed_out", "startup_failure"},
				Branches:    []string{"main"},
			},
		},
		Overlap: model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	schedules, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("schedule"))))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schedules), "trigger:") || !strings.Contains(string(schedules), "github_workflow_run:") {
		t.Fatalf("the schedules document does not carry the schedule as `trigger: {github_workflow_run: ...}`:\n%s", schedules)
	}
	// Stamped one schema back, as the dump the running build hands the
	// image it is upgrading to.
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion-1); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Run(ctx, &out, dir, Options{Migrate: true}); err != nil {
		t.Fatalf("state check -migrate: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "recurrence_kind") {
		t.Errorf("state check -migrate warned about recurrence_kind:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ok: grain can load this") {
		t.Errorf("state check -migrate did not say it can load the dump:\n%s", out.String())
	}
}
