package staterepo_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

const testOAuthSecret = "GOCSPX-do-not-commit-this-0123456789"

func storeWithOAuthSecret(t *testing.T, ctx context.Context, secret string) (*sql.DB, *model.Store) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	// Nothing in grain sets these columns any more (Google sign-in went
	// with docs/single-tenant-reset.md's stage 3d), but an upgraded
	// installation's row can still hold a secret until stage 6 drops
	// them, so they are written by hand.
	if _, err := db.ExecContext(ctx,
		"UPDATE `grain_config` SET `google_oauth_client_id` = ?, `google_oauth_client_secret` = ? WHERE `id` = 1",
		"client-id.apps.googleusercontent.com", secret); err != nil {
		t.Fatal(err)
	}
	return db, store
}

// configColumn is grain_config's column of that name, as stored.
func configColumn(t *testing.T, ctx context.Context, db *sql.DB, column string) string {
	t.Helper()
	var v string
	if err := db.QueryRowContext(ctx, "SELECT `"+column+"` FROM `grain_config` WHERE `id` = 1").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// noTraceOf fails if any file under dir holds needle.
func noTraceOf(t *testing.T, dir, needle string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), needle) {
			t.Errorf("%s holds %q", path, needle)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The Google OAuth client secret is credential material, and a state
// repository is a git repository agents clone: an export of a config
// that has one must not carry it, nor even the column's name.
func TestExportConfigWithholdsTheGoogleOAuthClientSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := storeWithOAuthSecret(t, ctx, testOAuthSecret)

	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	noTraceOf(t, dir, testOAuthSecret)
	noTraceOf(t, dir, "google_oauth_client_secret")

	// The rest of the row is still there.
	data, err := os.ReadFile(filepath.Join(dir, staterepo.TablesDir, "grain_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "client-id.apps.googleusercontent.com") {
		t.Errorf("grain_config.json lost the client id:\n%s", data)
	}
}

// The full Export is the host-local schema-upgrade handover, which never
// leaves the data directory; it has to carry the secret or an upgrade
// would lose it.
func TestExportKeepsTheGoogleOAuthClientSecretForTheUpgradeHandover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := storeWithOAuthSecret(t, ctx, testOAuthSecret)
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatal(err)
	}

	fresh, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := model.New(fresh).Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Import(ctx, fresh, dir); err != nil {
		t.Fatal(err)
	}
	if got := configColumn(t, ctx, fresh, "google_oauth_client_secret"); got != testOAuthSecret {
		t.Errorf("secret after the handover = %q, want %q", got, testOAuthSecret)
	}
}

// Applying a merged settings change replaces whole rows, and the file no
// longer carries the secret -- so the apply has to keep the running
// value rather than reset it, and a file that carries one anyway must
// not be a way to set it.
func TestApplyingSettingsKeepsTheRunningGoogleOAuthClientSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := storeWithOAuthSecret(t, ctx, testOAuthSecret)
	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, staterepo.TablesDir, "grain_config.json")

	edit := func(change func(row map[string]any)) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(data, &rows); err != nil {
			t.Fatal(err)
		}
		change(rows[0])
		out, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// An ordinary settings change merged in: the client id moves, the
	// secret stays.
	edit(func(row map[string]any) { row["google_oauth_client_id"] = "other-client.apps.googleusercontent.com" })
	if err := staterepo.ImportTables(ctx, db, dir, staterepo.SettingsTables); err != nil {
		t.Fatal(err)
	}
	if got := configColumn(t, ctx, db, "google_oauth_client_id"); got != "other-client.apps.googleusercontent.com" {
		t.Errorf("client id = %q, want the merged one", got)
	}
	if got := configColumn(t, ctx, db, "google_oauth_client_secret"); got != testOAuthSecret {
		t.Errorf("secret after applying = %q, want the running %q", got, testOAuthSecret)
	}

	// A file that names the column -- an old branch, or somebody writing
	// it in -- is not a way to set it.
	edit(func(row map[string]any) { row["google_oauth_client_secret"] = "planted" })
	if err := staterepo.ImportTables(ctx, db, dir, staterepo.SettingsTables); err != nil {
		t.Fatal(err)
	}
	if got := configColumn(t, ctx, db, "google_oauth_client_secret"); got != testOAuthSecret {
		t.Errorf("secret after applying a file that names it = %q, want the running %q",
			got, testOAuthSecret)
	}

	// And the check says so, without repeating the value, and without
	// warning that leaving the column out resets it.
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	scratch, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer scratch.Close()
	if err := model.New(scratch).Init(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := staterepo.Check(ctx, scratch, dir, model.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, w := range report.Warnings {
		if strings.Contains(w, "planted") {
			t.Errorf("a warning repeats the secret: %s", w)
		}
		if strings.Contains(w, `"google_oauth_client_secret"`) && strings.Contains(w, "never imported") {
			said = true
		}
	}
	if !said {
		t.Errorf("no warning that the file carries a withheld column; got %q", report.Warnings)
	}

	edit(func(row map[string]any) { delete(row, "google_oauth_client_secret") })
	report, err = staterepo.Check(ctx, scratch, dir, model.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range report.Warnings {
		if strings.Contains(w, "google_oauth_client_secret") {
			t.Errorf("a file that leaves the withheld column out is warned about: %s", w)
		}
	}
}
