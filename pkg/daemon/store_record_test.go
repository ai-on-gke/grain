package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

// TestRecordDatabaseConfigIsWhatResolveDatabaseReads: the Postgres
// database a daemon was started on is what a tool handed only -data-dir
// opens afterwards, password included, and none of it is written in
// clear text; SQLite, the default, records nothing.
func TestRecordDatabaseConfigIsWhatResolveDatabaseReads(t *testing.T) {
	for _, env := range []string{"GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}
	dataDir := t.TempDir()
	if err := RecordDatabaseConfig(dataDir, "sqlite", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, postgres.DatabaseConfigFile)); !os.IsNotExist(err) {
		t.Fatalf("sqlite recorded a %s: %v", postgres.DatabaseConfigFile, err)
	}
	if driver, dsn, err := ResolveDatabase(dataDir); err != nil || driver != "" || dsn != "" {
		t.Fatalf("ResolveDatabase with nothing recorded = %q, %q, %v", driver, dsn, err)
	}

	const dsn = "postgres://grain:s3cret@127.0.0.1/x?sslmode=disable"
	for i := 0; i < 2; i++ {
		if err := RecordDatabaseConfig(dataDir, "postgres", dsn); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, postgres.DatabaseConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Fatalf("%s holds the password in clear text: %s", postgres.DatabaseConfigFile, raw)
	}
	driver, got, err := ResolveDatabase(dataDir)
	if err != nil || driver != "postgres" || got != dsn {
		t.Fatalf("ResolveDatabase = %q, %q, %v; want postgres, %q", driver, got, err, dsn)
	}

	// The environment still wins over what is recorded, as it does for
	// the daemon itself.
	t.Setenv("GRAIN_DB_DRIVER", "sqlite")
	if driver, _, err := ResolveDatabase(dataDir); err != nil || driver != "sqlite" {
		t.Fatalf("ResolveDatabase with $GRAIN_DB_DRIVER=sqlite = %q, %v", driver, err)
	}
}
