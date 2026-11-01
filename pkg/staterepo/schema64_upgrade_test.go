package staterepo_test

// The upgrade from the last test plan's schema (docs/test-plans/2026-11-01.md
// §9, docs/test-runs/2026-11-01-schema-upgrade-65-78.md): a state repository
// dump at schema 64, taken from a real 8bdfa3a5 deployment, carried into this
// build's schema, and a store at an older schema exported the way
// `grain state prepare-upgrade` exports one.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// queryStrings is every row of a one-column query, as text.
func queryStrings(t *testing.T, db *sql.DB, q string) []string {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return out
}

// retiredGrants are the in-process capabilities migrations 67-82 rewrite.
var retiredGrants = []string{
	"team-broadcast", "dispatch-control", "pipeline-control", "memory", "release-notes",
	"self-upgrade", "test-quarantine", "grain-log", "deployment-health", "db-read", "db-write",
}

func assertNoRetiredGrant(t *testing.T, where string, names []string) {
	t.Helper()
	for _, name := range names {
		if slices.Contains(retiredGrants, name) {
			t.Errorf("%s still holds the retired %q after the import: %v", where, name, names)
		}
	}
}

func TestADumpAt64ImportsWithEveryMigrationsRewrite(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join("testdata", "upgrade-schema-64")
	store, db := openRealDB(t)
	if _, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion); err != nil {
		t.Fatalf("a dump at schema 64 does not import into %d: %v", model.SchemaVersion, err)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 78: the unslugged default is upgraded, once.
	if cfg.TaskBranchTemplate != model.DefaultTaskBranchTemplate {
		t.Errorf("task_branch_template = %q, want %q (migration 78)", cfg.TaskBranchTemplate, model.DefaultTaskBranchTemplate)
	}
	// 70, 66, 77: columns the dump predates take their defaults.
	if cfg.SettingsLocked {
		t.Error("settings_locked = true for a dump from before migration 70, want the default false")
	}
	// 67-82: the deployment's defaults, a repository's defaults and the
	// standing console sessions are rewritten just as an in-place
	// migration rewrites them.
	assertNoRetiredGrant(t, "grain_config.default_capabilities", cfg.DefaultCapabilities)
	for _, want := range []string{"api:memory", "api:dispatch-control", "api:deployment:read", model.GrainHealthGrant, model.GrainSQLReadGrant} {
		if !slices.Contains(cfg.DefaultCapabilities, want) {
			t.Errorf("grain_config.default_capabilities = %v, want %s in it", cfg.DefaultCapabilities, want)
		}
	}
	for _, list := range queryStrings(t, db, "SELECT `default_capabilities` FROM `repo_config` WHERE `owner` = 'acme' AND `name` = 'widgets'") {
		names := strings.Split(list, ",")
		assertNoRetiredGrant(t, "repo_config.default_capabilities", names)
		if !slices.Contains(names, model.GrainJournalGrant) {
			t.Errorf("repo_config.default_capabilities = %q, want %s (migration 80)", list, model.GrainJournalGrant)
		}
	}
	if len(cfg.ConsoleSessions) == 0 {
		t.Fatal("the dump's standing console sessions did not arrive")
	}
	for _, s := range cfg.ConsoleSessions {
		assertNoRetiredGrant(t, "console session "+s.ID, s.Capabilities)
		held := 0
		for _, c := range s.Capabilities {
			if c == "api:memory" {
				held++
			}
		}
		if s.ID == "ops" && held != 1 {
			t.Errorf("console session ops holds api:memory %d times, want once: %v", held, s.Capabilities)
		}
	}
	// 65: the issue importer the dump predates is off.
	for _, v := range queryStrings(t, db, "SELECT `issue_import_enabled` || ',' || `issue_import_interval_seconds` FROM `repo_config`") {
		if v != "0,0" {
			t.Errorf("repo_config issue import = %s for a dump from before migration 65, want 0,0", v)
		}
	}
	// 73, 74: template and schedule rows take the defaults.
	for _, v := range queryStrings(t, db, "SELECT `id` || ':' || `priority` || ',' || `keep_running` FROM `template`") {
		if !strings.HasSuffix(v, ":0,0") {
			t.Errorf("template %s, want priority 0 and keep_running 0", v)
		}
	}
	for _, grant := range queryStrings(t, db, "SELECT `capability` FROM `template_grant` UNION ALL SELECT `capability` FROM `schedule_grant`") {
		assertNoRetiredGrant(t, "template/schedule grants", []string{grant})
	}
}

// TestAStoreFromBeforeKeepRunningExportsSchedulesThisBuildCanImport is
// `grain state prepare-upgrade` on a store at an older schema: this build
// exports the old store's rows and rehearses importing them. A column the
// old store has no such thing as -- schedule.keep_running, template.priority
// -- must be left out of the document, not written as null, which the
// NOT NULL column then refuses ("NOT NULL constraint failed:
// schedule.keep_running" from a schema-65 deployment).
func TestAStoreFromBeforeKeepRunningExportsSchedulesThisBuildCanImport(t *testing.T) {
	ctx := context.Background()
	_, old := openRealDB(t)
	// The views read these columns; SQLite will not drop one a view uses.
	for _, view := range queryStrings(t, old, "SELECT `name` FROM `sqlite_master` WHERE `type` = 'view'") {
		if _, err := old.Exec("DROP VIEW `" + view + "`"); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		"ALTER TABLE `schedule` DROP COLUMN `keep_running`",
		"ALTER TABLE `template` DROP COLUMN `keep_running`",
		"ALTER TABLE `template` DROP COLUMN `priority`",
		"INSERT INTO `template` (`id`, `name`, `title`, `body`, `target_owner`, `target_name`, `auto_merge`, `created_at`) " +
			"VALUES ('tpl-1', 'nightly', 'Nightly', 'b', 'acme', 'widgets', 0, '2026-10-31 10:00:00 +0000 UTC')",
		"INSERT INTO `schedule` (`id`, `title`, `body`, `target_owner`, `target_name`, `auto_merge`, `recurrence_kind`, " +
			"`time_of_day_minutes`, `enabled`, `next_run_at`, `created_at`) VALUES ('sch-1', 'Daily', 'b', 'acme', 'widgets', 0, " +
			"'daily', 180, 1, '2026-11-02 03:00:00 +0000 UTC', '2026-10-31 10:00:00 +0000 UTC')",
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, old, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 73); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{"schedules.yml", "templates.yml"} {
		body, err := os.ReadFile(filepath.Join(dir, "settings", doc))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"keep-running:", "priority:"} {
			if strings.Contains(string(body), key) {
				t.Errorf("settings/%s writes %s for a store with no such column:\n%s", doc, key, body)
			}
		}
	}
	_, db := openRealDB(t)
	if _, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion); err != nil {
		t.Fatalf("this build cannot take the rows it exported from a store at schema 73: %v", err)
	}
	if got := queryStrings(t, db, "SELECT `id` || ':' || `keep_running` FROM `schedule` WHERE `id` = 'sch-1'"); !slices.Equal(got, []string{"sch-1:0"}) {
		t.Errorf("schedule keep_running = %v, want [sch-1:0]", got)
	}
	if got := queryStrings(t, db, "SELECT `id` || ':' || `priority` || ',' || `keep_running` FROM `template` WHERE `id` = 'tpl-1'"); !slices.Equal(got, []string{"tpl-1:0,0"}) {
		t.Errorf("template priority, keep_running = %v, want [tpl-1:0,0]", got)
	}
}
