package postgres_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/postgres/postgrestest"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// TestResetWithNullKeyOnPostgres tests that staterepo.TableResetsWithColumnsForTest / rowKey / rowResets
// on PostgreSQL correctly handles rows where a column in the key is NULL, using
// "IS NOT DISTINCT FROM ?" to locate the existing database row and compute reset values.
func TestResetWithNullKeyOnPostgres(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	ctx := context.Background()
	_, db := postgrestest.Open(t)

	// In SQLite a primary key column can be NULL, whereas in PostgreSQL columns in a
	// PRIMARY KEY constraint are implicitly NOT NULL. To test rowKey's NULL key matching
	// ("k IS NOT DISTINCT FROM ?") on Postgres, we create a table with a nullable key column
	// and invoke TableResetsWithColumnsForTest where key columns include a column holding NULL.
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE test_resets_null_key (
			part_a TEXT,
			part_b TEXT,
			value TEXT NOT NULL DEFAULT 'default_val'
		)
	`); err != nil {
		t.Fatalf("creating test table: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO test_resets_null_key (part_a, part_b, value) VALUES ('group-1', NULL, 'custom_val')
	`); err != nil {
		t.Fatalf("inserting test row with NULL in key: %v", err)
	}

	cols := []staterepo.ColumnForTest{
		{Name: "part_a", DeclType: "TEXT", NotNull: false, PK: 1},
		{Name: "part_b", DeclType: "TEXT", NotNull: false, PK: 2},
		{Name: "value", DeclType: "TEXT", NotNull: true, DefaultVal: "'default_val'", PK: 0},
	}

	incomingRows := []map[string]any{
		{
			"part_a": "group-1",
			"part_b": nil, // NULL key column!
		},
	}

	resets, err := staterepo.TableResetsWithColumnsForTest(ctx, db, "test_resets_null_key", cols, incomingRows)
	if err != nil {
		t.Fatalf("TableResetsWithColumnsForTest failed: %v", err)
	}

	if len(resets) != 1 {
		t.Fatalf("expected 1 reset matching the row with NULL in key, got %d: %+v", len(resets), resets)
	}
	r := resets[0]
	if r.Column != "value" || r.From != "'custom_val'" || !strings.HasPrefix(r.To, "'default_val'") {
		t.Errorf("unexpected reset details: %+v", r)
	}
}

// seedComprehensiveSettings populates all the settings tables requested by Section 5:
// OAuth config, teams with git connections, schedules, templates, repo configs, and team repo configs.
func seedComprehensiveSettings(t *testing.T, ctx context.Context, store *model.Store, oauthSecret string) {
	t.Helper()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	// 1. Config with OAuth
	cfg := model.DefaultConfig()
	cfg.EnvironmentName = "grain-prod"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	setOAuthColumns(t, ctx, store, "google-client-id.apps.googleusercontent.com", oauthSecret)

	// 2. RepoConfig
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                repo,
		DefaultCapabilities: []string{"grain-ci", "gitproxy"},
		SetupCommand:        "make setup",
	}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	// 3. Templates
	for _, tid := range []string{"deploy-prod", "run-tests"} {
		if err := store.PutTemplate(ctx, model.Template{
			ID:        tid,
			Name:      tid,
			Title:     "Template " + tid,
			Body:      "echo 'running " + tid + "'",
			CreatedAt: now,
		}); err != nil {
			t.Fatalf("PutTemplate %s: %v", tid, err)
		}
	}

	// 4. Schedules
	tid := "deploy-prod"
	if err := store.PutSchedule(ctx, model.Schedule{
		ID:         "sched-nightly",
		Title:      "Nightly Deploy",
		TemplateID: &tid,
		Target:     repo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 22 * 60},
		Overlap:    model.OverlapSkip,
		Enabled:    true,
		NextRunAt:  now,
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}

	// 5. Suites
	if err := store.PutSuite(ctx, model.Suite{
		ID:        "suite-qa",
		Name:      "qa-suite",
		Mode:      model.SuiteCount,
		Count:     2,
		Items:     []model.SuiteItem{{TemplateID: "run-tests"}},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("PutSuite: %v", err)
	}

	// 6. A team, and the deployment's GitConnections (grain_config's
	// since schema 22)
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "cred-github-global"},
			{Credential: "cred-internal-ghe", Host: "ghe.internal.net", Scope: "infra/*"},
		}
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	// 7. A repository's own git credential (team_repo_config's until
	// schema 23).
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:               model.RepoRef{Owner: "acme", Name: "credentialed"},
		GitCredential:      "cred-github-global",
		WatchDefaultBranch: true,
	}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}
}

// compareDirsRecursively checks that dir1 and dir2 have identical files and contents.
func compareDirsRecursively(t *testing.T, dir1, dir2 string) {
	t.Helper()
	var files1, files2 []string
	walk := func(root string, target *[]string) error {
		return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				rel, _ := filepath.Rel(root, p)
				*target = append(*target, rel)
			}
			return nil
		})
	}
	if err := walk(dir1, &files1); err != nil {
		t.Fatalf("walking %s: %v", dir1, err)
	}
	if err := walk(dir2, &files2); err != nil {
		t.Fatalf("walking %s: %v", dir2, err)
	}

	if !reflect.DeepEqual(files1, files2) {
		t.Fatalf("exported file lists differ:\n dir1: %v\n dir2: %v", files1, files2)
	}

	for _, f := range files1 {
		content1, err := os.ReadFile(filepath.Join(dir1, f))
		if err != nil {
			t.Fatalf("reading %s in dir1: %v", f, err)
		}
		content2, err := os.ReadFile(filepath.Join(dir2, f))
		if err != nil {
			t.Fatalf("reading %s in dir2: %v", f, err)
		}
		if string(content1) != string(content2) {
			t.Fatalf("file %s differs between exports!\n--- dir1 ---\n%s\n--- dir2 ---\n%s", f, content1, content2)
		}
	}
}

// TestSettingsRoundTripPostgresToPostgres exports a populated Postgres store,
// imports into an empty Postgres store, exports again, and verifies byte-for-byte identity.
func TestSettingsRoundTripPostgresToPostgres(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	ctx := context.Background()

	// Store 1: Populated Postgres store
	store1, db1 := postgrestest.Open(t)
	const secret = "GOCSPX-secret-roundtrip-pg"
	seedComprehensiveSettings(t, ctx, store1, secret)

	dir1 := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db1, dir1); err != nil {
		t.Fatalf("exporting from store1: %v", err)
	}

	// Store 2: Fresh empty Postgres store
	_, db2 := postgrestest.Open(t)

	// Import into Store 2
	if err := staterepo.ImportTables(ctx, db2, dir1, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing into store2: %v", err)
	}

	// Export from Store 2
	dir2 := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db2, dir2); err != nil {
		t.Fatalf("exporting from store2: %v", err)
	}

	// Verify byte-for-byte identical
	compareDirsRecursively(t, dir1, dir2)
}

// TestFailedSettingsApplyLeavesDaemonServing verifies that when Apply encounters
// a broken settings commit (e.g. malformed JSON), StateManager.cycle returns ErrNotApplied,
// preserves daemon serving capability without crash/panic, records the failure,
// and surfaces it in Status().
func TestFailedSettingsApplyLeavesDaemonServing(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	store, db := postgrestest.Open(t)
	seedComprehensiveSettings(t, ctx, store, "GOCSPX-daemon-test")

	remote := filepath.Join(t.TempDir(), "remote.git")
	runCmd(t, "", "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)

	dataDir := t.TempDir()
	stateDir := daemon.StateRepoDir(dataDir)
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: stateDir, Remote: remote})
	if err != nil {
		t.Fatalf("opening state repo: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if _, err := staterepo.SyncAll(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	mgr := daemon.NewStateManager(dataDir, db, repo, daemon.OpenSecrets(dataDir), nil, nil)

	// Introduce a broken JSON commit in remote
	cloneDir := filepath.Join(t.TempDir(), "clone")
	runCmd(t, "", "git", "clone", "--quiet", remote, cloneDir)
	cfgPath := filepath.Join(cloneDir, staterepo.TablesDir, "grain_config.json")
	if err := os.WriteFile(cfgPath, []byte("NOT_VALID_JSON{{{"), 0644); err != nil {
		t.Fatalf("writing broken json: %v", err)
	}
	runCmd(t, cloneDir, "git", "config", "user.name", "Test")
	runCmd(t, cloneDir, "git", "config", "user.email", "test@example.com")
	runCmd(t, cloneDir, "git", "commit", "-am", "Break grain_config.json")
	runCmd(t, cloneDir, "git", "push", "--quiet", "origin", "main")

	// Trigger sync (which calls cycle(ctx, true))
	_, err = mgr.Sync(ctx)
	if err == nil {
		t.Fatal("expected sync to return error on broken json, got nil")
	}
	if !errors.Is(err, staterepo.ErrNotApplied) {
		t.Fatalf("expected ErrNotApplied, got: %v", err)
	}

	// Daemon continues serving: verify database is intact and queryable
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("store is unreadable after apply failure: %v", err)
	}
	if cfg.EnvironmentName != "grain-prod" {
		t.Fatalf("store data altered after failed apply: %v", cfg.EnvironmentName)
	}

	// Status() reports the error
	status, err := mgr.Status(ctx)
	if err != nil {
		t.Fatalf("Status() failed: %v", err)
	}
	if status.Error == "" {
		t.Fatal("Status().Error is empty; expected failure to be reported")
	}
}

func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %s %v failed: %v\nOutput: %s", name, args, err, out)
	}
}
