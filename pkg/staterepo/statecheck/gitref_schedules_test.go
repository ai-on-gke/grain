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

// `grain state check` is what a grain/config pull request runs, and it
// used to answer "ok: grain can load this" for a settings/schedules.yml the
// running deployment would refuse at POST /api/schedules (#744;
// docs/test-runs/2026-11-07-releases-upgrades-ci.md, Bug F5): a malformed
// git_ref repo, a retired grain/config-<team> alias, and -- with
// -data-dir -- the encrypted secrets backup repository.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// exportWatching is a state repository holding one enabled git_ref
// schedule watching acme/upstream-watched, ready for a test to edit.
func exportWatching(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()
	store := model.New(db)
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Watch upstream",
		Target: model.RepoRef{Owner: "acme", Name: "widgets"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceGitRef,
			GitRef: &model.GitRefTrigger{Repo: "acme/upstream-watched", Ref: "refs/heads/main"}},
		Overlap: model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	return dir
}

func editSchedules(t *testing.T, dir string, pairs ...string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("schedule")))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for i := 0; i+1 < len(pairs); i += 2 {
		edited := strings.Replace(s, pairs[i], pairs[i+1], 1)
		if edited == s {
			t.Fatalf("%s does not hold %q:\n%s", path, pairs[i], s)
		}
		s = edited
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStateCheckRefusesForbiddenGitRefSchedules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	secretsDataDir := t.TempDir()
	if err := secretsrepo.SaveSettings(secretsDataDir, secretsrepo.Settings{
		Remote: "https://github.com/acme/host-secrets.git", Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		edits   []string
		dataDir string
		wantSub string // "" means the check passes
	}{
		{"allowed watch", nil, "", ""},
		{"malformed repo", []string{"repo: acme/upstream-watched", `repo: "not a repo"`}, "",
			"git_ref trigger needs `repo: owner/name`"},
		{"retired grain/config alias", []string{"repo: acme/upstream-watched", "repo: grain/config-alpha"}, "",
			"grain/config-alpha not found: only grain/config exists"},
		{"retired alias paused is permitted",
			[]string{"repo: acme/upstream-watched", "repo: grain/config-alpha", "enabled: true", "enabled: false"}, "", ""},
		{"secrets backup repo without -data-dir", []string{"repo: acme/upstream-watched", "repo: acme/host-secrets"}, "", ""},
		{"secrets backup repo with -data-dir", []string{"repo: acme/upstream-watched", "repo: acme/host-secrets"}, secretsDataDir,
			"encrypted secrets backup repository"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := exportWatching(t)
			if len(tc.edits) > 0 {
				editSchedules(t, dir, tc.edits...)
			}
			var out bytes.Buffer
			err := Run(ctx, &out, dir, Options{DataDir: tc.dataDir})
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("state check = %v, want ok\n%s", err, out.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("state check = %v, want an error containing %q\n%s", err, tc.wantSub, out.String())
			}
		})
	}
}
