package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/postgres"
	"github.com/bwsalmon/grain/pkg/model/postgres/postgrestest"
	"github.com/bwsalmon/grain/pkg/ui"
)

// A bundle from a Postgres deployment carries db-config.json, not the
// database's rows, so the store `grain import` has to check is the
// database that file names. It used to open the embedded SQLite store
// instead, creating an empty one at this build's schema in the data
// directory and reporting "schema version: <this build's>" for a
// database still one schema back (grain/task-4870). The database's own
// schema is reported now: a current one is printed, an older one is
// refused naming prepare-upgrade, and neither leaves a SQLite store.
func TestImportOfAPostgresBundleChecksThePostgresDatabase(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	for _, env := range []string{"GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}
	prev := model.DefaultRepoHost()
	t.Cleanup(func() { model.SetDefaultGitHost(prev) })
	dsn, _ := postgresTestSchemaDSN(t)

	srcDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Setenv("GRAIN_DB_DRIVER", "postgres")
	os.Setenv("GRAIN_DB_DSN", dsn)
	seedTestConnections(t, srcDir, "git:\n  - credential: anonymous\n")
	os.Setenv("GRAIN_DB_DRIVER", "")
	os.Setenv("GRAIN_DB_DSN", "")
	seedTestAccessCode(t, srcDir)

	// The first start records the database in db-config.json, which is
	// what the bundle carries.
	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: srcDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
			DbDriver: "postgres", DbDSN: dsn,
		})
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Filed on Postgres before the export", Repo: "owner/payments-api",
	}); err != nil {
		cancel()
		<-runErr
		t.Fatalf("filing a task: %v", err)
	}
	cancel()
	<-runErr

	bundle := filepath.Join(t.TempDir(), "migration.tar.gz")
	var exported bytes.Buffer
	if err := runExport(context.Background(), []string{"-data-dir", srcDir, "-out", bundle}, &exported); err != nil {
		t.Fatalf("exporting the Postgres deployment: %v", err)
	}
	if !strings.Contains(exported.String(), "database: postgres") {
		t.Fatalf("export printed %q, want a postgres bundle", exported.String())
	}

	importInto := func(t *testing.T) (string, string, error) {
		t.Helper()
		dataDir := filepath.Join(t.TempDir(), "imported")
		var out bytes.Buffer
		err := runImport(context.Background(), []string{"-data-dir", dataDir, "-in", bundle}, &out)
		if _, serr := os.Stat(filepath.Join(dataDir, "store")); !os.IsNotExist(serr) {
			t.Errorf("import left a SQLite store in a data directory that runs on postgres (%v)", serr)
		}
		return dataDir, out.String(), err
	}

	t.Run("current schema", func(t *testing.T) {
		_, out, err := importInto(t)
		if err != nil {
			t.Fatalf("importing a Postgres bundle at this build's schema: %v", err)
		}
		if want := "schema version: " + strconv.Itoa(model.SchemaVersion) + "\n"; !strings.Contains(out, want) {
			t.Errorf("import printed:\n%s\nwant a line containing %q", out, want)
		}
	})

	t.Run("previous schema", func(t *testing.T) {
		db, err := postgres.Open(postgres.Config{URL: dsn})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec("UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", model.SchemaVersion-1); err != nil {
			t.Fatalf("stamping the database with the older schema: %v", err)
		}
		dataDir, out, err := importInto(t)
		if err == nil {
			t.Fatalf("import of a Postgres bundle whose database is one schema back succeeded:\n%s", out)
		}
		for _, want := range []string{
			"schema " + strconv.Itoa(model.SchemaVersion-1),
			"grain state -data-dir " + dataDir + " prepare-upgrade",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("import refused with %q, want it to name %q", err, want)
			}
		}
		if _, serr := os.Stat(daemon.SchemaUpgradeDir(dataDir)); !os.IsNotExist(serr) {
			t.Errorf("import armed an upgrade for a database it refused (%v)", serr)
		}
	})
}
