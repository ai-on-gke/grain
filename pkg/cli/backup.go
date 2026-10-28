// backup.go implements the operator-facing and scheduled halves of backups.
// Both call pkg/backup.Service, so a backup made by hand is byte-for-byte the
// same shape as one made by the daemon.
package cli

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
	grainsqlite "github.com/ai-on-gke/grain/pkg/model/sqlite"
)

const backupUsage = `usage: grain backup -data-dir DIR [flags]

Create an online snapshot of the Grain database and a restorable grain/config
repository bundle, then upload it to Google Drive or a Google Cloud Storage
bucket. Credentials and the data-dir/secrets directory are never included.

Flags:
  -gcs-bucket NAME              upload to this GCS bucket instead of Drive
  -google-drive-folder ID       Drive destination folder ID (default "root")
  -google-credentials-file FILE
                                Google service-account or OAuth JSON; omitted
                                uses Application Default Credentials
`

func backupCmd(args []string) {
	err := runBackup(context.Background(), args, os.Stdout)
	if code := backupExitCode(err); code != 0 {
		fmt.Fprintln(os.Stderr, "grain backup: "+err.Error())
		os.Exit(code)
	}
}

// backupExitCode is the status `grain backup` exits with after runBackup
// returned err. -h has already printed the usage it asked for, so it is
// a success rather than "grain backup: flag: help requested" and a 1.
func backupExitCode(err error) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 1
}

func runBackup(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain backup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, backupUsage) }
	dataDir := fs.String("data-dir", dataDirDefault(), "root directory a colocated grain daemon uses (required; $"+dataDirEnvVar+" supplies the default)")
	gcsBucket := fs.String("gcs-bucket", "", "GCS destination bucket; when set, upload to GCS instead of Google Drive")
	folderID := fs.String("google-drive-folder", "root", "Google Drive destination folder ID")
	credentialsFile := fs.String("google-credentials-file", "", "Google credential JSON; omitted uses Application Default Credentials")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *dataDir == "" {
		fs.Usage()
		return errors.New("-data-dir is required")
	}
	var db *sql.DB
	if os.Getenv("GRAIN_DB_DRIVER") == "postgres" || os.Getenv("GRAIN_POSTGRES_URL") != "" || os.Getenv("DATABASE_URL") != "" {
		var err error
		db, err = daemon.OpenRawDB(*dataDir)
		if err != nil {
			return fmt.Errorf("opening database: %w", err)
		}
	} else if saved, _ := postgres.ReadDatabaseConfig(*dataDir); saved != nil && saved.Driver == "postgres" {
		var err error
		db, err = daemon.OpenRawDB(*dataDir)
		if err != nil {
			return fmt.Errorf("opening database: %w", err)
		}
	} else {
		databasePath := filepath.Join(*dataDir, "store", "grain.db")
		if _, err := os.Stat(databasePath); err != nil {
			return fmt.Errorf("finding database %s: %w", databasePath, err)
		}
		var err error
		db, err = grainsqlite.Open(grainsqlite.DefaultConfig(filepath.Join(*dataDir, "store")))
		if err != nil {
			return fmt.Errorf("opening database: %w", err)
		}
	}
	defer db.Close()
	service, destination, err := daemon.BackupService(ctx, *gcsBucket, *folderID, *credentialsFile)
	if err != nil {
		return err
	}
	result, err := service.Run(ctx, db, daemon.StateRepoDir(*dataDir))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "backed up %d bytes to %s as %s", result.Size, destination, result.Name)
	if result.Remote.URL != "" {
		fmt.Fprintf(out, " (%s)", result.Remote.URL)
	} else if result.Remote.ID != "" {
		fmt.Fprintf(out, " (%s)", result.Remote.ID)
	}
	fmt.Fprintln(out)
	return nil
}
