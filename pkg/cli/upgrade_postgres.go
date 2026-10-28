package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
	grainsqlite "github.com/ai-on-gke/grain/pkg/model/sqlite"
)

const upgradePostgresUsage = `usage: grain upgrade-postgres [flags]

Upgrade an existing SQLite database into PostgreSQL and migrate all tables and data.
Optionally writes db-config.json so subsequent daemon startups use PostgreSQL.
A password in the DSN is kept in grain's encrypted secret store (secret
"database-password"), never in db-config.json itself.

Flags:
  -data-dir DIR     root directory of the grain installation (defaults to $GRAIN_DATA_DIR, then /etc/grain/cli.env's, e.g. /var/lib/grain/data)
  -sqlite-path PATH path to the source SQLite database file (defaults to <data-dir>/store)
  -dsn URL          target PostgreSQL connection URL or DSN (e.g. postgres://user:pass@host:5432/dbname?sslmode=disable)
  -no-data          initialize schema in PostgreSQL only; do not migrate existing SQLite rows
  -no-save-config   do not persist the database configuration in <data-dir>/db-config.json
`

func upgradePostgresCmd(args []string) {
	if err := runUpgradePostgres(context.Background(), args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "grain upgrade-postgres: "+err.Error())
		os.Exit(1)
	}
}

func runUpgradePostgres(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain upgrade-postgres", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, upgradePostgresUsage) }

	dataDir := fs.String("data-dir", dataDirDefault(), "root data directory")
	sqlitePath := fs.String("sqlite-path", "", "source SQLite database file path")
	dsn := fs.String("dsn", "", "target PostgreSQL DSN / connection URL")
	noData := fs.Bool("no-data", false, "skip migrating existing data rows")
	noSaveConfig := fs.Bool("no-save-config", false, "do not save db-config.json in data-dir")

	if err := fs.Parse(args); err != nil {
		return err
	}

	targetDSN := strings.TrimSpace(*dsn)
	var pgCfg postgres.Config
	if targetDSN != "" {
		pgCfg = postgres.DefaultConfig(targetDSN)
	} else if postgres.IsAvailable() {
		if *dataDir == "" {
			return errors.New("either -dsn or -data-dir is required when using embedded postgres")
		}
		cfg, stop, err := postgres.EnsureRunning(ctx, *dataDir)
		if err != nil {
			return fmt.Errorf("starting embedded postgres: %w", err)
		}
		defer stop()
		pgCfg = cfg
		targetDSN = cfg.URL
	} else {
		return errors.New("-dsn is required (no embedded postgres found)")
	}

	srcPath := *sqlitePath
	if srcPath == "" {
		if *dataDir == "" {
			return errors.New("either -sqlite-path or -data-dir is required")
		}
		srcPath = filepath.Join(*dataDir, "store")
	}

	sqliteDB, err := grainsqlite.Open(grainsqlite.DefaultConfig(srcPath))
	if err != nil {
		return fmt.Errorf("opening source sqlite database %s: %w", srcPath, err)
	}
	defer sqliteDB.Close()

	fmt.Fprintf(out, "Upgrading database from SQLite (%s) to PostgreSQL...\n", srcPath)
	res, err := postgres.UpgradeFromSQLite(ctx, sqliteDB, pgCfg, !*noData)
	if err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}

	if !*noSaveConfig && *dataDir != "" {
		if err := postgres.SaveDatabaseConfig(*dataDir, "postgres", targetDSN, daemon.OpenSecrets(*dataDir)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save db config: %v\n", err)
		} else {
			fmt.Fprintf(out, "Saved database config to %s/db-config.json\n", *dataDir)
			if _, _, hasPassword := postgres.SplitDSNPassword(targetDSN); hasPassword {
				fmt.Fprintf(out, "Stored the database password in the secret store as %q, not in db-config.json\n",
					postgres.DatabasePasswordSecret)
			}
		}
	}

	fmt.Fprintf(out, "Successfully upgraded database to PostgreSQL (%d tables migrated in %v)\n", res.TablesMigrated, res.Duration)
	return nil
}
