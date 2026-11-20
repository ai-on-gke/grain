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

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureDaemonLogs(t *testing.T) *lockedLogBuffer {
	t.Helper()
	orig := log.Writer()
	buf := &lockedLogBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(orig)
	})
	return buf
}

func runGitConfigRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Operator",
		"GIT_AUTHOR_EMAIL=operator@example.com",
		"GIT_COMMITTER_NAME=Operator",
		"GIT_COMMITTER_EMAIL=operator@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mergeConfigPR(t *testing.T, dir, branch, subject string, edit func(dir string)) string {
	t.Helper()
	runGitConfigRepo(t, dir, "checkout", "-b", branch)
	edit(dir)
	runGitConfigRepo(t, dir, "add", "-A")
	runGitConfigRepo(t, dir, "commit", "-m", subject)
	leaf := runGitConfigRepo(t, dir, "rev-parse", "HEAD")
	runGitConfigRepo(t, dir, "checkout", staterepo.DefaultBranch)
	runGitConfigRepo(t, dir, "merge", "--no-ff", "-m", "Merge pull request from "+branch, branch)
	runGitConfigRepo(t, dir, "branch", "-D", branch)
	return leaf
}

func exportTriggerSchedulesDoc(t *testing.T, ctx context.Context, sched model.Schedule) []byte {
	t.Helper()
	tmp := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(tmp, "scratch")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := model.New(db)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	dumpDir := filepath.Join(tmp, "dump")
	if err := staterepo.ExportConfig(ctx, db, dumpDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dumpDir, "settings", "schedules.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func exportTemplatesDoc(t *testing.T, ctx context.Context, tpls ...model.Template) []byte {
	t.Helper()
	tmp := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(tmp, "scratch")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := model.New(db)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tpl := range tpls {
		if err := s.PutTemplate(ctx, tpl); err != nil {
			t.Fatal(err)
		}
	}
	dumpDir := filepath.Join(tmp, "dump")
	if err := staterepo.ExportConfig(ctx, db, dumpDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dumpDir, "settings", "templates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestSchemaUpgradeReappliesUnappliedStateRepoCommits reproduces the schema
// 85 -> SchemaVersion upgrade scenario where merged human commits in
// grain/config had failed to apply on the old daemon (leaving grain-loaded-head
// behind HEAD) and verifies that the upgrade applies them onto the migrated
// database rather than silently overwriting them.
func TestSchemaUpgradeReappliesUnappliedStateRepoCommits(t *testing.T) {
	for _, withUpgradeNote := range []bool{true, false} {
		name := "plain_binary_swap"
		if withUpgradeNote {
			name = "with_prepare_upgrade_note"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dataDir := t.TempDir()
			const oldSchema = 85

			buildStoreAtSchema(t, dataDir, oldSchema, func(t *testing.T, db *sql.DB) {
				if _, err := db.ExecContext(ctx,
					"UPDATE `template` SET `name` = 'existing-template', `title` = 'Existing Template', `body` = 'Keep this template' WHERE `id` = 'v2-tpl'"); err != nil {
					t.Fatal(err)
				}
			})

			repo, err := OpenStateRepo(ctx, dataDir)
			if err != nil {
				t.Fatal(err)
			}
			oldDB, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
			if err != nil {
				t.Fatal(err)
			}
			if err := staterepo.ExportConfig(ctx, oldDB, repo.Dir()); err != nil {
				oldDB.Close()
				t.Fatal(err)
			}
			if err := staterepo.WriteSchemaVersion(repo.Dir(), oldSchema); err != nil {
				oldDB.Close()
				t.Fatal(err)
			}
			if _, err := repo.Commit(ctx, "Export grain state at schema 85"); err != nil {
				oldDB.Close()
				t.Fatal(err)
			}
			syncedHead, err := repo.Head(ctx)
			if err != nil {
				oldDB.Close()
				t.Fatal(err)
			}
			if err := repo.SetLoadedHead(ctx, syncedHead); err != nil {
				oldDB.Close()
				t.Fatal(err)
			}

			now := time.Date(2026, 11, 3, 5, 0, 0, 0, time.UTC)
			schedDoc := exportTriggerSchedulesDoc(t, ctx, model.Schedule{
				ID:     "sched-post-upgrade-check",
				Title:  "Post-upgrade verification",
				Body:   "Check deployment health after upgrade.",
				Target: model.RepoRef{Owner: "acme", Name: "widgets"},
				Recurrence: model.Recurrence{
					Kind: model.RecurrenceGitHubWorkflowRun,
					GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
						Workflow:    "deploy.yml",
						Conclusions: []string{"success"},
					},
				},
				Overlap:   model.OverlapSkip,
				Enabled:   true,
				NextRunAt: now,
				CreatedAt: now,
			})

			// Merge three human config commits past syncedHead without advancing
			// grain-loaded-head (simulating failed Apply + failed final sync on
			// schema 85, including an event-triggered schedule).
			mergeConfigPR(t, repo.Dir(), "pr-event-schedule", "config: add workflow-run schedule", func(dir string) {
				p := filepath.Join(dir, "settings", "schedules.yml")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, schedDoc, 0o644); err != nil {
					t.Fatal(err)
				}
			})

			templatesPath := filepath.Join(repo.Dir(), "settings", "templates.yml")
			rawTpl, err := os.ReadFile(templatesPath)
			if err != nil {
				oldDB.Close()
				t.Fatal(err)
			}
			editedTpl := strings.Replace(string(rawTpl), "Existing Template", "Existing Template Edited", 1)
			if err := os.WriteFile(templatesPath, []byte(editedTpl), 0o644); err != nil {
				oldDB.Close()
				t.Fatal(err)
			}
			runGitConfigRepo(t, repo.Dir(), "add", "-A")
			runGitConfigRepo(t, repo.Dir(), "commit", "-m", "config: update existing-template title")

			secondTemplatesDoc := exportTemplatesDoc(t, ctx,
				model.Template{
					ID:    "v2-tpl",
					Name:  "existing-template",
					Title: "Existing Template Edited",
					Body:  "Keep this template",
				},
				model.Template{
					ID:    "tpl-incident-review",
					Name:  "incident-review",
					Title: "Incident Review",
					Body:  "Review recent incident.",
				},
			)
			mergeConfigPR(t, repo.Dir(), "pr-second-template", "config: add incident-review template", func(dir string) {
				p := filepath.Join(dir, "settings", "templates.yml")
				if werr := os.WriteFile(p, secondTemplatesDoc, 0o644); werr != nil {
					t.Fatal(werr)
				}
			})

			if withUpgradeNote {
				if _, err := WriteSchemaUpgrade(ctx, dataDir, oldDB, "main", oldSchema, model.SchemaVersion); err != nil {
					oldDB.Close()
					t.Fatal(err)
				}
			}
			if err := oldDB.Close(); err != nil {
				t.Fatal(err)
			}

			logs := captureDaemonLogs(t)

			store, db, migrated, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
			if err != nil {
				t.Fatalf("openStoreForSchemaUpgrade: %v", err)
			}
			defer db.Close()
			if !migrated.any() {
				t.Fatal("expected storeMigration to record migration")
			}

			if err := loadStateRepo(ctx, dataDir, repo, db, migrated); err != nil {
				t.Fatalf("loadStateRepo: %v", err)
			}
			discardSchemaUpgrade(dataDir)

			// All three human commits must be present in both store and state-repo.
			templates, err := store.ListTemplates(ctx)
			if err != nil {
				t.Fatal(err)
			}
			byName := map[string]model.Template{}
			for _, tpl := range templates {
				byName[tpl.Name] = tpl
			}
			if got := byName["existing-template"].Title; got != "Existing Template Edited" {
				t.Errorf("existing-template title = %q, want %q", got, "Existing Template Edited")
			}
			if _, ok := byName["incident-review"]; !ok {
				t.Errorf("incident-review template missing after upgrade")
			}

			gotSched, err := store.GetSchedule(ctx, "sched-post-upgrade-check")
			if err != nil {
				t.Fatal(err)
			}
			if gotSched == nil {
				t.Errorf("sched-post-upgrade-check schedule missing after upgrade")
			}

			rewrittenSchedules, err := os.ReadFile(filepath.Join(repo.Dir(), "settings", "schedules.yml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(rewrittenSchedules), "sched-post-upgrade-check") {
				t.Errorf("rewritten state-repo schedules.yml dropped sched-post-upgrade-check:\n%s", rewrittenSchedules)
			}

			logOut := logs.String()
			if !strings.Contains(logOut, "applied 3 previously unapplied config commit(s)") {
				t.Errorf("expected daemon log to note 3 applied commits, got:\n%s", logOut)
			}

			// No unapplied-commit warning notification should be open when all
			// pending commits applied cleanly.
			openNotifs, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range openNotifs {
				if n.Family == model.FamilyStateRepoUnapplied {
					t.Errorf("unexpected %s notification when commits applied cleanly: %+v", n.Family, n)
				}
			}
		})
	}
}

// TestSchemaUpgradeLogsAndRaisesNotificationWhenUnappliedCommitsStillFail
// verifies that when unapplied commits in grain/config still cannot be imported
// even at the new schema, the daemon starts cleanly, names every dropped commit
// in the log and migration commit message, and raises an open operator
// notification under pipeline.staterepo-unapplied.
func TestSchemaUpgradeLogsAndRaisesNotificationWhenUnappliedCommitsStillFail(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	const oldSchema = 85

	buildStoreAtSchema(t, dataDir, oldSchema, nil)

	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	oldDB, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
	if err != nil {
		t.Fatal(err)
	}
	if err := staterepo.ExportConfig(ctx, oldDB, repo.Dir()); err != nil {
		oldDB.Close()
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(repo.Dir(), oldSchema); err != nil {
		oldDB.Close()
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "Export grain state at schema 85"); err != nil {
		oldDB.Close()
		t.Fatal(err)
	}
	syncedHead, err := repo.Head(ctx)
	if err != nil {
		oldDB.Close()
		t.Fatal(err)
	}
	if err := repo.SetLoadedHead(ctx, syncedHead); err != nil {
		oldDB.Close()
		t.Fatal(err)
	}
	if err := oldDB.Close(); err != nil {
		t.Fatal(err)
	}

	c1 := mergeConfigPR(t, repo.Dir(), "pr-1", "config: first unapplied change", func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("updated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	c2 := mergeConfigPR(t, repo.Dir(), "pr-2", "config: invalid schedules syntax", func(dir string) {
		p := filepath.Join(dir, "settings", "schedules.yml")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("schedules: [\n  broken\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	priorHead, err := repo.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	logs := captureDaemonLogs(t)

	store, db, migrated, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStoreForSchemaUpgrade: %v", err)
	}
	defer db.Close()

	// Subscribe a durable steward task to pipeline.* before loadStateRepo to
	// verify NotificationSubscribers matches FamilyStateRepoUnapplied.
	stewardID := "steward-1"
	nowTask := time.Now().UTC()
	if err := store.PutTask(ctx, model.Task{
		ID:          stewardID,
		Title:       "Pipeline steward",
		Body:        "Watch pipeline notifications",
		KeepRunning: true,
		CreatedAt:   &nowTask,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskNotificationSubscriptions(ctx, stewardID, model.PipelineStewardSubscriptions); err != nil {
		t.Fatal(err)
	}

	if err := loadStateRepo(ctx, dataDir, repo, db, migrated); err != nil {
		t.Fatalf("loadStateRepo: %v", err)
	}
	discardSchemaUpgrade(dataDir)

	logOut := logs.String()
	if !strings.Contains(logOut, "WARNING: 2 unapplied config commit(s)") ||
		!strings.Contains(logOut, "config: first unapplied change") ||
		!strings.Contains(logOut, "config: invalid schedules syntax") {
		t.Errorf("expected WARNING naming both dropped commits in daemon log, got:\n%s", logOut)
	}

	commitMsg := runGitConfigRepo(t, repo.Dir(), "log", "-1", "--format=%B")
	if !strings.Contains(commitMsg, "WARNING:") ||
		!strings.Contains(commitMsg, c1[:7]) ||
		!strings.Contains(commitMsg, c2[:7]) {
		t.Errorf("expected migration commit message to list dropped commits, got:\n%s", commitMsg)
	}

	subj := model.NotificationSubject(model.FamilyStateRepoUnapplied, shortCommit(priorHead))
	notif, err := store.OpenNotification(ctx, subj)
	if err != nil {
		t.Fatal(err)
	}
	if notif == nil {
		t.Fatalf("expected open notification %s, got nil", subj)
	}
	if notif.Severity != model.NotificationWarning {
		t.Errorf("notification severity = %q, want %q", notif.Severity, model.NotificationWarning)
	}
	if notif.Repo != "grain/config" {
		t.Errorf("notification repo = %q, want grain/config", notif.Repo)
	}
	if !strings.Contains(notif.Body, "config: first unapplied change") ||
		!strings.Contains(notif.Body, "config: invalid schedules syntax") ||
		!strings.Contains(notif.Body, "Apply error:") {
		t.Errorf("notification body missing dropped commits or error:\n%s", notif.Body)
	}
	subs, err := store.NotificationSubscribers(ctx, model.FamilyStateRepoUnapplied)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0] != stewardID {
		t.Errorf("expected pipeline.* steward %s to subscribe to %s, got %v", stewardID, model.FamilyStateRepoUnapplied, subs)
	}
}

// TestSchemaUpgradeStoreRebuildRewindsLoadedHeadToSnapshot verifies that when
// openStoreForSchemaUpgrade rebuilds a store from schema-upgrade/ (for example,
// rolling back from a newer store schema) after another config commit was
// synced between WriteSchemaUpgrade and shutdown, importSchemaUpgrade rewinds
// grain-loaded-head to note.LoadedHead so loadStateRepo re-applies that commit.
func TestSchemaUpgradeStoreRebuildRewindsLoadedHeadToSnapshot(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	// Create a live store at SchemaVersion, seed state-repo, and write an
	// upgrade note from futureSchema -> SchemaVersion before stamping the live
	// store at futureSchema to exercise the archive+rebuild path.
	futureSchema := model.SchemaVersion + 1
	store0, db0, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store0.PutTemplate(ctx, model.Template{
		ID:    "tpl-initial",
		Name:  "initial-tpl",
		Title: "Initial",
		Body:  "Prompt",
	}); err != nil {
		db0.Close()
		t.Fatal(err)
	}
	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		db0.Close()
		t.Fatal(err)
	}
	if err := staterepo.Load(ctx, repo, db0, model.SchemaVersion); err != nil {
		db0.Close()
		t.Fatal(err)
	}

	// Snapshot db0 into schema-upgrade/ via an operator-authored downgrade
	// note while grain-loaded-head is at the initial commit (WriteSchemaUpgrade
	// refuses downgrades).
	if _, err := WriteSchemaDowngrade(ctx, dataDir, db0, futureSchema, model.SchemaVersion); err != nil {
		db0.Close()
		t.Fatal(err)
	}

	// Simulate a config commit merged AND applied into db0 after
	// WriteSchemaUpgrade wrote its snapshot (advancing grain-loaded-head),
	// right before the old daemon stamps/closes at futureSchema.
	lateTemplatesDoc := exportTemplatesDoc(t, ctx,
		model.Template{
			ID:    "tpl-initial",
			Name:  "initial-tpl",
			Title: "Initial",
			Body:  "Prompt",
		},
		model.Template{
			ID:    "tpl-late-merged",
			Name:  "late-merged-tpl",
			Title: "Late Merged",
			Body:  "Merged after WriteSchemaUpgrade.",
		},
	)
	mergeConfigPR(t, repo.Dir(), "pr-late-merge", "config: late merge after snapshot", func(dir string) {
		p := filepath.Join(dir, "settings", "templates.yml")
		if werr := os.WriteFile(p, lateTemplatesDoc, 0o644); werr != nil {
			t.Fatal(werr)
		}
	})
	if _, err := staterepo.Apply(ctx, repo, db0, model.SchemaVersion); err != nil {
		db0.Close()
		t.Fatal(err)
	}
	// Stamp both state-repo and db0 at futureSchema so openStoreForSchemaUpgrade
	// archives db0 and rebuilds from schema-upgrade/, and loadStateRepo runs
	// migrateStateRepo.
	if err := staterepo.WriteSchemaVersion(repo.Dir(), futureSchema); err != nil {
		db0.Close()
		t.Fatal(err)
	}
	runGitConfigRepo(t, repo.Dir(), "add", "-A")
	runGitConfigRepo(t, repo.Dir(), "commit", "-m", "Stamp state at future schema")
	futureHead, err := repo.Head(ctx)
	if err != nil {
		db0.Close()
		t.Fatal(err)
	}
	if err := repo.SetLoadedHead(ctx, futureHead); err != nil {
		db0.Close()
		t.Fatal(err)
	}
	if _, err := db0.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", futureSchema); err != nil {
		db0.Close()
		t.Fatal(err)
	}
	if err := db0.Close(); err != nil {
		t.Fatal(err)
	}

	store, db, migrated, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStoreForSchemaUpgrade: %v", err)
	}
	defer db.Close()

	if err := loadStateRepo(ctx, dataDir, repo, db, migrated); err != nil {
		t.Fatalf("loadStateRepo: %v", err)
	}
	discardSchemaUpgrade(dataDir)

	templates, err := store.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundLate := false
	for _, tpl := range templates {
		if tpl.Name == "late-merged-tpl" {
			foundLate = true
		}
	}
	if !foundLate {
		t.Fatalf("expected late-merged-tpl to be re-applied into rebuilt store, got %+v", templates)
	}
}
