package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/postgres"
	"github.com/bwsalmon/grain/pkg/model/postgres/postgrestest"
	"github.com/bwsalmon/grain/pkg/staterepo"
	"github.com/bwsalmon/grain/pkg/ui"
)

// A Postgres store at the schema before this build's has the same way
// forward a SQLite one has (grain/task-4851): `grain state prepare-upgrade`
// exports it, and the next start keeps the old tables in a schema of
// their own and rebuilds the store from their rows. Before, prepare-upgrade
// answered "none" for anything but the embedded store and the next start
// refused again, so a Postgres deployment had no way across a bump at all.
func TestPrepareUpgradeCarriesAPostgresStoreAcrossASchemaChange(t *testing.T) {
	if model.V2BaselineSchemaVersion-1 < model.BaselineSchemaVersion {
		t.Skip("this build is at the single-tenant baseline, so the schema before it is one no build migrates from")
	}
	postgrestest.SkipUnlessAvailable(t)
	for _, env := range []string{"GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}
	dsn, schema := postgresTestSchemaDSN(t)

	dataDir := t.TempDir()
	if err := os.MkdirAll(dataDir+"/secrets/github", 0o755); err != nil {
		t.Fatal(err)
	}
	// Seeded into the Postgres store through the environment; the daemon
	// records the database in db-config.json at its first start, and that
	// is what prepare-upgrade below finds it by.
	os.Setenv("GRAIN_DB_DRIVER", "postgres")
	os.Setenv("GRAIN_DB_DSN", dsn)
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	os.Setenv("GRAIN_DB_DRIVER", "")
	os.Setenv("GRAIN_DB_DSN", "")
	seedTestAccessCode(t, dataDir)
	runDaemon := func(t *testing.T) (context.Context, string, func()) {
		uiAddr := freeTCPAddr(t)
		ctx, cancel := context.WithCancel(context.Background())
		runErr := make(chan error, 1)
		go func() {
			runErr <- daemon.Run(ctx, daemon.Config{
				DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
				GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
				DbDriver: "postgres", DbDSN: dsn,
			})
		}()
		return ctx, uiAddr, func() { cancel(); <-runErr }
	}

	ctx, uiAddr, stop := runDaemon(t)
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Filed on Postgres before the schema change", Repo: "owner/payments-api",
	})
	if err != nil {
		stop()
		t.Fatalf("filing a task: %v", err)
	}
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != 200 {
		stop()
		t.Fatalf("sync returned %d: %s", code, body)
	}
	stop()

	// The store and the state repository become the previous schema's.
	db, err := postgres.Open(postgres.Config{URL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", model.V2BaselineSchemaVersion-1); err != nil {
		t.Fatalf("stamping the store with the older schema: %v", err)
	}
	db.Close()
	work := daemon.StateRepoDir(dataDir)
	if err := staterepo.WriteSchemaVersion(work, model.V2BaselineSchemaVersion-1); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "--all", ".")
	gitIn(t, work, "-c", "user.email=grain@the-build-before", "-c", "user.name=grain",
		"commit", "-m", "Update grain state")

	var out bytes.Buffer
	if err := statePrepareUpgrade(context.Background(), dataDir, &out, nil); err != nil {
		t.Fatalf("prepare-upgrade: %v", err)
	}
	want := fmt.Sprintf("prepared %d %d %s\n", model.V2BaselineSchemaVersion-1, model.SchemaVersion,
		daemon.SchemaUpgradeDir(dataDir))
	if out.String() != want {
		t.Fatalf("prepare-upgrade printed %q, want %q", out.String(), want)
	}

	ctx, uiAddr, stop = runDaemon(t)
	defer stop()
	client = ui.NewHTTPClient("http://" + uiAddr)
	waitForUI(t, ctx, client)
	task, err := client.Task(ctx, created.ID)
	if err != nil {
		t.Fatalf("the task filed before the schema change did not survive it: %v", err)
	}
	if task.Title != "Filed on Postgres before the schema change" {
		t.Errorf("the task came back as %q", task.Title)
	}

	// The old tables are kept, rows and all, beside the new store.
	admin, err := postgres.Open(postgres.Config{URL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	prefix := schema + "_schema" + strconv.Itoa(model.V2BaselineSchemaVersion-1) + "_"
	var archive string
	if err := admin.QueryRow(`SELECT nspname::text FROM pg_namespace WHERE starts_with(nspname, ?)`,
		prefix).Scan(&archive); err != nil {
		t.Fatalf("finding the schema the old store was kept in: %v", err)
	}
	var kept int
	if err := admin.QueryRow(fmt.Sprintf(`SELECT count(*) FROM "%s"."task" WHERE "title" = ?`, archive),
		"Filed on Postgres before the schema change").Scan(&kept); err != nil || kept != 1 {
		t.Errorf("the old store's task row is not in %s (%d rows, %v)", archive, kept, err)
	}
	if _, err := os.Stat(daemon.SchemaUpgradeDir(dataDir)); !os.IsNotExist(err) {
		t.Errorf("the upgrade note was not spent (%v)", err)
	}
}

// postgresTestSchemaDSN makes a schema of its own on the test server and
// returns a DSN whose search_path is that schema alone, for a daemon to
// be started on the way an operator starts one with -postgres-url.
func postgresTestSchemaDSN(t *testing.T) (dsn, schema string) {
	t.Helper()
	cfg := postgrestest.Config(t)
	admin, err := postgres.Open(cfg)
	if err != nil {
		t.Skipf("connecting to the test postgres: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema = "prep_" + hex.EncodeToString(b)
	if _, err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)); err != nil {
		admin.Close()
		t.Fatalf("creating schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		rows, err := admin.Query(`SELECT nspname::text FROM pg_namespace WHERE starts_with(nspname, ?)`, schema)
		if err == nil {
			var names []string
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					names = append(names, n)
				}
			}
			rows.Close()
			for _, n := range names {
				_, _ = admin.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, n))
			}
		}
		admin.Close()
	})

	var u *url.URL
	if cfg.URL != "" {
		if u, err = url.Parse(cfg.URL); err != nil {
			t.Fatal(err)
		}
	} else {
		u = &url.URL{Scheme: "postgres", User: url.User(cfg.User), Path: "/" + cfg.Database}
		q := u.Query()
		q.Set("host", cfg.Host)
		q.Set("port", strconv.Itoa(cfg.Port))
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), schema
}
