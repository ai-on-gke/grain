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

package postgres_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/backup"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

type captureDestination struct {
	name      string
	mediaType string
	content   []byte
}

func (d *captureDestination) Upload(_ context.Context, name, mediaType string, content io.Reader) (backup.Remote, error) {
	d.name, d.mediaType = name, mediaType
	var err error
	d.content, err = io.ReadAll(content)
	return backup.Remote{ID: "pg-backup-id", URL: "https://storage.example/pg-backup.zip"}, err
}

func initBackupConfigRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("config", "user.name", "test")
	runGit("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "secrets.enc"), []byte("legacy secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "secrets.enc")
	runGit("commit", "-m", "legacy")
	if err := os.Remove(filepath.Join(repo, "secrets.enc")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "config.yaml"), []byte("defaultModel: gemini\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "-A")
	runGit("commit", "-m", "current config")
	return repo
}

func unzipArchive(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("opening backup zip: %v", err)
	}
	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = data
	}
	return files
}

func randomIdentifier(t *testing.T, prefix string) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func TestBackupSnapshotsAndRestoresPostgresDatabase(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	if _, err := postgres.FindBinary("pg_dump"); err != nil {
		t.Skipf("pg_dump not available: %v", err)
	}
	pgRestorePath, err := postgres.FindBinary("pg_restore")
	if err != nil {
		t.Skipf("pg_restore not available: %v", err)
	}

	ctx := context.Background()
	baseCfg := postgrestest.Config(t)
	adminDB, err := postgres.Open(baseCfg)
	if err != nil {
		t.Fatalf("opening admin db: %v", err)
	}
	t.Cleanup(func() {
		_ = adminDB.Close()
	})

	srcDBName := randomIdentifier(t, "grain_backup_src")
	dstDBName := randomIdentifier(t, "grain_backup_dst")
	roleName := randomIdentifier(t, "grain_backup_role")
	rolePass := "S3cr3tBackupPass_2026"

	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, srcDBName))
		_, _ = adminDB.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, dstDBName))
		_, _ = adminDB.ExecContext(context.Background(), fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, roleName))
	})
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE ROLE "%s" LOGIN PASSWORD '%s'`, roleName, rolePass),
		fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, srcDBName, roleName),
		fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, dstDBName, roleName),
	} {
		if _, err := adminDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("setup SQL (%s): %v", stmt, err)
		}
	}

	srcCfg := baseCfg
	srcCfg.Database = srcDBName
	srcCfg.User = roleName
	srcCfg.Password = rolePass

	srcDB, err := postgres.Open(srcCfg)
	if err != nil {
		t.Fatalf("opening source database: %v", err)
	}
	defer srcDB.Close()

	if _, err := srcDB.ExecContext(ctx, strings.Join(postgres.Statements(), ";\n")); err != nil {
		t.Fatalf("applying postgres DDL: %v", err)
	}
	srcStore := model.New(srcDB)
	if err := srcStore.Init(ctx); err != nil {
		t.Fatalf("initializing source store: %v", err)
	}
	taskID, err := srcStore.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("NewTaskID: %v", err)
	}
	backupAt := time.Date(2026, 11, 7, 5, 30, 0, 0, time.UTC)
	srcTask := task(taskID, true)
	srcTask.Title = "Verify Postgres backup snapshot"
	srcTask.Body = "Created before pg_dump runs"
	if err := srcStore.PutTask(ctx, srcTask); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	configRepo := initBackupConfigRepo(t)
	dest := &captureDestination{}
	svc := backup.Service{
		Destination: dest,
		Now:         func() time.Time { return backupAt },
	}
	res, err := svc.Run(ctx, srcDB, configRepo)
	if err != nil {
		t.Fatalf("backup.Service.Run on Postgres: %v", err)
	}
	if res.Database != "database/grain.dump" {
		t.Fatalf("Result.Database = %q, want database/grain.dump", res.Database)
	}

	files := unzipArchive(t, dest.content)
	var gotNames []string
	for k := range files {
		gotNames = append(gotNames, k)
	}
	slices.Sort(gotNames)
	wantNames := []string{"config/config-repo.bundle", "database/grain.dump", "manifest.json"}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("archive entries = %v, want %v", gotNames, wantNames)
	}
	if bytes.Contains(dest.content, []byte(rolePass)) {
		t.Fatal("backup archive leaked database password")
	}

	var manifest backup.Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}
	if manifest.FormatVersion != backup.FormatVersion || manifest.Database != "database/grain.dump" || manifest.DatabaseDriver != "postgres" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}

	// Restore database/grain.dump into dstDBName using pg_restore and read the task back.
	dumpFile := filepath.Join(t.TempDir(), "grain.dump")
	if err := os.WriteFile(dumpFile, files["database/grain.dump"], 0o600); err != nil {
		t.Fatal(err)
	}
	dstCfg := srcCfg
	dstCfg.Database = dstDBName
	restoreTarget := connURI(t, dstCfg, false)
	restoreCmd := exec.CommandContext(ctx, pgRestorePath,
		"--no-password",
		"--no-owner",
		"--dbname="+restoreTarget,
		dumpFile,
	)
	restoreCmd.Env = append(os.Environ(), "PGPASSWORD="+rolePass)
	if out, err := restoreCmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_restore failed: %v\n%s", err, out)
	}

	dstDB, err := postgres.Open(dstCfg)
	if err != nil {
		t.Fatalf("opening restored database: %v", err)
	}
	defer dstDB.Close()

	dstStore := model.New(dstDB)
	restoredTask, err := dstStore.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask from restored Postgres store: %v", err)
	}
	if restoredTask == nil || restoredTask.Title != "Verify Postgres backup snapshot" {
		t.Fatalf("restored task = %+v, want title %q", restoredTask, "Verify Postgres backup snapshot")
	}

	// Also verify explicit PostgresDSN override (used by `grain backup` CLI and
	// scheduled daemon backups).
	destDSN := &captureDestination{}
	dsnRes, err := (backup.Service{
		Destination: destDSN,
		PostgresDSN: connURI(t, srcCfg, true),
		Now:         func() time.Time { return backupAt },
	}).Run(ctx, srcDB, configRepo)
	if err != nil {
		t.Fatalf("backup.Service.Run with PostgresDSN override: %v", err)
	}
	if dsnRes.Database != "database/grain.dump" {
		t.Fatalf("Result.Database = %q, want database/grain.dump", dsnRes.Database)
	}
	if bytes.Contains(destDSN.content, []byte(rolePass)) {
		t.Fatal("backup archive leaked database password from PostgresDSN")
	}
}

func connURI(t *testing.T, cfg postgres.Config, withPassword bool) string {
	t.Helper()
	var u *url.URL
	if cfg.URL != "" {
		parsed, err := url.Parse(cfg.URL)
		if err != nil {
			t.Fatalf("parsing cfg.URL: %v", err)
		}
		u = parsed
	} else {
		host := cfg.Host
		if host == "" {
			host = "localhost"
		}
		port := cfg.Port
		if port <= 0 {
			port = 5432
		}
		q := url.Values{}
		if strings.HasPrefix(host, "/") {
			q.Set("host", host)
			if cfg.Port > 0 && cfg.Port != 5432 {
				q.Set("port", strconv.Itoa(cfg.Port))
			}
			q.Set("sslmode", "disable")
			u = &url.URL{Scheme: "postgres", RawQuery: q.Encode()}
		} else {
			if cfg.SSLMode != "" {
				q.Set("sslmode", cfg.SSLMode)
			} else if host == "localhost" || host == "127.0.0.1" {
				q.Set("sslmode", "disable")
			}
			u = &url.URL{
				Scheme:   "postgres",
				Host:     net.JoinHostPort(host, strconv.Itoa(port)),
				RawQuery: q.Encode(),
			}
		}
	}
	if withPassword && cfg.Password != "" {
		u.User = url.UserPassword(cfg.User, cfg.Password)
	} else if cfg.User != "" {
		u.User = url.User(cfg.User)
	}
	if cfg.Database != "" {
		u.Path = "/" + cfg.Database
	}
	return u.String()
}

func TestBackupSnapshotsIsolatedSchemaPostgres(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	if _, err := postgres.FindBinary("pg_dump"); err != nil {
		t.Skipf("pg_dump not available: %v", err)
	}
	pgRestorePath, err := postgres.FindBinary("pg_restore")
	if err != nil {
		t.Skipf("pg_restore not available: %v", err)
	}

	ctx := context.Background()
	store, db := postgrestest.Open(t)
	taskID, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("NewTaskID: %v", err)
	}
	isoTask := task(taskID, true)
	isoTask.Title = "Isolated schema task"
	if err := store.PutTask(ctx, isoTask); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	configRepo := initBackupConfigRepo(t)
	dest := &captureDestination{}
	res, err := (backup.Service{Destination: dest}).Run(ctx, db, configRepo)
	if err != nil {
		t.Fatalf("backup.Service.Run on isolated schema: %v", err)
	}
	if res.Database != "database/grain.dump" {
		t.Fatalf("Result.Database = %q, want database/grain.dump", res.Database)
	}

	files := unzipArchive(t, dest.content)
	dumpFile := filepath.Join(t.TempDir(), "isolated.dump")
	if err := os.WriteFile(dumpFile, files["database/grain.dump"], 0o600); err != nil {
		t.Fatal(err)
	}
	listOut, err := exec.CommandContext(ctx, pgRestorePath, "--list", dumpFile).CombinedOutput()
	if err != nil {
		t.Fatalf("pg_restore --list: %v\n%s", err, listOut)
	}
	if !bytes.Contains(listOut, []byte("TABLE DATA")) || !bytes.Contains(listOut, []byte("task")) {
		t.Fatalf("pg_restore --list missing task table data:\n%s", listOut)
	}
}
