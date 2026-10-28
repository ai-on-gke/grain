// Package backup creates portable, credential-free snapshots of a Grain
// installation and sends them to a configured destination.
//
// A backup contains the live SQLite database and a restorable bundle of the
// controller-local grain/config repository. It deliberately does not walk the
// data directory: credentials live under data-dir/secrets. Before bundling the
// repository, its refs are exported through a temporary bare repository with
// legacy secrets.enc paths removed from every commit.
package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// FormatVersion is the archive layout Manifest.FormatVersion records.
	FormatVersion = 1
	mediaTypeZIP  = "application/zip"
)

// Destination stores a completed backup. Implementations own only transport;
// archive contents and credential exclusion stay here.
type Destination interface {
	Upload(ctx context.Context, name, mediaType string, content io.Reader) (Remote, error)
}

// Remote identifies the stored object without requiring every future
// destination to have Google Drive's URL shape.
type Remote struct {
	ID  string
	URL string
}

// Result describes one completed backup.
type Result struct {
	Name       string
	Remote     Remote
	Size       int64
	Database   string
	ConfigHead string
	CreatedAt  time.Time
}

// Service creates and uploads one backup.
type Service struct {
	Destination Destination
	// TempDir is optional. Empty uses the operating system's temporary
	// directory. Temporary material is mode 0700 and removed after each run.
	TempDir string
	// Now is replaceable for deterministic names and manifests in tests.
	Now func() time.Time
}

// Run snapshots db and the committed HEAD of configRepo, packages both, and
// uploads the result. It never reads any other path under the data directory.
func (s Service) Run(ctx context.Context, db *sql.DB, configRepo string) (Result, error) {
	if db == nil {
		return Result{}, fmt.Errorf("backup: no database configured")
	}
	if s.Destination == nil {
		return Result{}, fmt.Errorf("backup: no destination configured")
	}
	if strings.TrimSpace(configRepo) == "" {
		return Result{}, fmt.Errorf("backup: no config repository configured")
	}

	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	created := now().UTC()
	name := "grain-backup-" + created.Format("20060102T150405Z") + ".zip"

	dir, err := os.MkdirTemp(s.TempDir, "grain-backup-")
	if err != nil {
		return Result{}, fmt.Errorf("backup: creating temporary directory: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("backup: protecting temporary directory: %w", err)
	}

	databasePath := filepath.Join(dir, "grain.db")
	if err := snapshotDatabase(ctx, db, databasePath); err != nil {
		return Result{}, err
	}

	configPath := filepath.Join(dir, "config-repo.bundle")
	head, err := archiveConfigRepo(ctx, configRepo, configPath)
	if err != nil {
		return Result{}, err
	}

	manifest := Manifest{
		FormatVersion: FormatVersion,
		CreatedAt:     created,
		Database:      "database/grain.db",
		ConfigRepo:    "config/config-repo.bundle",
		ConfigHead:    head,
	}
	archivePath := filepath.Join(dir, name)
	if err := writeArchive(archivePath, databasePath, configPath, manifest); err != nil {
		return Result{}, err
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return Result{}, fmt.Errorf("backup: opening archive: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Result{}, fmt.Errorf("backup: sizing archive: %w", err)
	}
	remote, err := s.Destination.Upload(ctx, name, mediaTypeZIP, f)
	if err != nil {
		return Result{}, fmt.Errorf("backup: uploading %s: %w", name, err)
	}
	return Result{
		Name: name, Remote: remote, Size: info.Size(), Database: manifest.Database,
		ConfigHead: head, CreatedAt: created,
	}, nil
}

// Manifest is stored as manifest.json at the root of every archive.
type Manifest struct {
	FormatVersion int       `json:"formatVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	Database      string    `json:"database"`
	ConfigRepo    string    `json:"configRepository"`
	ConfigHead    string    `json:"configHead"`
}

// ErrPostgresNotSupported is returned when database snapshotting is attempted on Postgres.
var ErrPostgresNotSupported = errors.New("backup: database snapshotting is not supported on Postgres (VACUUM INTO requires SQLite; use pg_dump or database-native backups)")

func isPostgres(ctx context.Context, db *sql.DB) bool {
	var one int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM pg_database LIMIT 1").Scan(&one)
	return err == nil
}

func snapshotDatabase(ctx context.Context, db *sql.DB, destination string) error {
	if isPostgres(ctx, db) {
		return ErrPostgresNotSupported
	}
	// VACUUM INTO is SQLite's online backup primitive: it reads one
	// consistent snapshot while writers continue through WAL and produces a
	// self-contained database rather than copying grain.db without its WAL.
	// SQLite parameters are not accepted in VACUUM's filename position.
	quoted := "'" + strings.ReplaceAll(destination, "'", "''") + "'"
	if _, err := db.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("backup: snapshotting database: %w", err)
	}
	return nil
}

func archiveConfigRepo(ctx context.Context, repo, destination string) (string, error) {
	headRef, err := gitOutput(ctx, repo, "symbolic-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("backup: reading config repository branch: %w", err)
	}

	// Do not copy .git directly. Its config can contain credential-bearing
	// remote URLs, and its objects may retain the encrypted secrets file
	// written by old Grain releases after that file disappeared from HEAD.
	// fast-export/fast-import keeps every branch, tag, commit, and author but
	// rewrites the graph with that path absent everywhere.
	sanitized := destination + ".repo"
	if _, err := gitOutput(ctx, "", "init", "--bare", "--", sanitized); err != nil {
		return "", fmt.Errorf("backup: preparing sanitized config repository: %w", err)
	}
	exporter := exec.CommandContext(ctx, "git", "fast-export", "--signed-tags=strip", "--all",
		"--", ".", ":(exclude)secrets.enc")
	exporter.Dir = repo
	importer := exec.CommandContext(ctx, fastImport[0], fastImport[1:]...)
	importer.Dir = sanitized

	pipe, err := exporter.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("backup: connecting config repository export: %w", err)
	}
	var exportErr, importErr bytes.Buffer
	exporter.Stderr = &exportErr
	importer.Stderr = &importErr
	importer.Stdin = pipe
	if err := importer.Start(); err != nil {
		pipe.Close()
		return "", fmt.Errorf("backup: starting config repository import: %w", err)
	}
	// The importer holds its own copy of the read end now. Keeping this
	// one open until exporter.Wait would mean an importer that dies early
	// never sends the exporter EPIPE: it fills the pipe buffer and blocks
	// until ctx is cancelled, which for a backup with no deadline is never.
	pipe.Close()
	if err := exporter.Start(); err != nil {
		_ = importer.Wait()
		return "", fmt.Errorf("backup: starting config repository export: %w", err)
	}
	exportWaitErr := exporter.Wait()
	importWaitErr := importer.Wait()
	if exportWaitErr != nil {
		return "", fmt.Errorf("backup: exporting config repository: %w: %s",
			exportWaitErr, strings.TrimSpace(exportErr.String()))
	}
	if importWaitErr != nil {
		return "", fmt.Errorf("backup: sanitizing config repository: %w: %s",
			importWaitErr, strings.TrimSpace(importErr.String()))
	}
	if _, err := gitOutput(ctx, sanitized, "symbolic-ref", "--end-of-options", "HEAD", strings.TrimSpace(headRef)); err != nil {
		return "", fmt.Errorf("backup: restoring config repository branch: %w", err)
	}
	head, err := gitOutput(ctx, sanitized, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("backup: reading sanitized config repository HEAD: %w", err)
	}
	if _, err := gitOutput(ctx, sanitized, "bundle", "create", "--end-of-options", destination, "--all"); err != nil {
		return "", fmt.Errorf("backup: bundling config repository: %w", err)
	}
	return strings.TrimSpace(head), nil
}

// fastImport is the command line that writes the sanitized repository,
// held in a variable so a test can stand in an importer that fails.
var fastImport = []string{"git", "fast-import", "--quiet"}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func writeArchive(destination, databasePath, configPath string, manifest Manifest) (err error) {
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("backup: creating archive: %w", err)
	}
	z := zip.NewWriter(out)
	defer func() {
		if closeErr := z.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("backup: closing archive: %w", closeErr)
		}
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("backup: closing archive file: %w", closeErr)
		}
	}()

	m, err := z.Create("manifest.json")
	if err != nil {
		return fmt.Errorf("backup: adding manifest: %w", err)
	}
	if err := json.NewEncoder(m).Encode(manifest); err != nil {
		return fmt.Errorf("backup: writing manifest: %w", err)
	}
	for _, file := range []struct {
		name string
		path string
	}{
		{"database/grain.db", databasePath},
		{"config/config-repo.bundle", configPath},
	} {
		if err := addFile(z, file.name, file.path); err != nil {
			return err
		}
	}
	return nil
}

func addFile(z *zip.Writer, name, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("backup: opening %s: %w", name, err)
	}
	defer src.Close()
	dst, err := z.Create(name)
	if err != nil {
		return fmt.Errorf("backup: adding %s: %w", name, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("backup: writing %s: %w", name, err)
	}
	return nil
}
