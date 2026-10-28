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

package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	grainsqlite "github.com/ai-on-gke/grain/pkg/model/sqlite"
)

type memoryDestination struct {
	name      string
	mediaType string
	content   []byte
}

func (d *memoryDestination) Upload(_ context.Context, name, mediaType string, content io.Reader) (Remote, error) {
	d.name, d.mediaType = name, mediaType
	var err error
	d.content, err = io.ReadAll(content)
	return Remote{ID: "remote-id", URL: "https://drive.example/file"}, err
}

func TestRunBacksUpDatabaseAndCurrentConfigWithoutCredentials(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE example (value TEXT); INSERT INTO example VALUES ('kept')"); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(root, "state-repo")
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.name", "test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	// A legacy encrypted credential in history and a credential-bearing
	// remote config are both absent because backups use a filtered git bundle,
	// not an unfiltered copy of .git.
	writeTestFile(t, filepath.Join(repo, "secrets.enc"), "old credential")
	runGit(t, repo, "add", "secrets.enc")
	runGit(t, repo, "commit", "-m", "old state")
	if err := os.Remove(filepath.Join(repo, "secrets.enc")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "settings.json"), "{\"kept\":true}\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "current config")
	runGit(t, repo, "branch", "open-proposal")
	runGit(t, repo, "remote", "add", "origin", "https://token@example.invalid/config.git")
	// The real credential store is beside the two selected sources. Its
	// presence makes accidental data-directory traversal visible.
	writeTestFile(t, filepath.Join(root, "secrets", "credentials.json"), "live credential")

	at := time.Date(2026, 9, 14, 12, 30, 45, 0, time.UTC)
	destination := &memoryDestination{}
	got, err := (Service{Destination: destination, Now: func() time.Time { return at }}).Run(ctx, db, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "grain-backup-20260914T123045Z.zip" || destination.name != got.Name {
		t.Fatalf("backup name = %q, uploaded as %q", got.Name, destination.name)
	}
	if destination.mediaType != "application/zip" || got.Remote.ID != "remote-id" {
		t.Fatalf("upload metadata = %q, %+v", destination.mediaType, got.Remote)
	}

	files := unzip(t, destination.content)
	if names := sortedKeys(files); !slices.Equal(names, []string{
		"config/config-repo.bundle", "database/grain.db", "manifest.json",
	}) {
		t.Fatalf("archive files = %v", names)
	}
	var manifest Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != FormatVersion || manifest.CreatedAt != at || manifest.ConfigHead == "" {
		t.Fatalf("manifest = %+v", manifest)
	}
	assertSnapshotRow(t, files["database/grain.db"])
	assertConfigBundle(t, files["config/config-repo.bundle"])
}

func TestRunValidatesItsSources(t *testing.T) {
	service := Service{}
	if _, err := service.Run(context.Background(), nil, "repo"); err == nil {
		t.Fatal("Run with no database succeeded")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := service.Run(context.Background(), db, "repo"); err == nil {
		t.Fatal("Run with no destination succeeded")
	}
}

// An importer that dies before reading its input must fail the backup
// rather than leave fast-export blocked on a full pipe nobody reads.
func TestRunFailsWhenTheImportDiesEarly(t *testing.T) {
	root := t.TempDir()
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := filepath.Join(root, "state-repo")
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.name", "test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	// Far more than a pipe buffer, so the exporter cannot finish by
	// writing everything into the kernel and exiting.
	big := make([]byte, 4<<20)
	for i := range big {
		big[i] = byte(i*7 + i/251)
	}
	writeTestFile(t, filepath.Join(repo, "big.bin"), string(big))
	runGit(t, repo, "add", "big.bin")
	runGit(t, repo, "commit", "-m", "big")

	original := fastImport
	t.Cleanup(func() { fastImport = original })
	fastImport = []string{"sh", "-c", "exit 1"}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = (Service{Destination: &memoryDestination{}}).Run(ctx, db, repo)
	if err == nil {
		t.Fatal("Run succeeded with an importer that failed")
	}
	if ctx.Err() != nil {
		t.Fatalf("Run only returned once its context ran out: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if args[0] == "init" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func unzip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range r.File {
		in, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name], err = io.ReadAll(in)
		in.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func assertConfigBundle(t *testing.T, data []byte) {
	t.Helper()
	root := t.TempDir()
	bundle := filepath.Join(root, "config.bundle")
	if err := os.WriteFile(bundle, data, 0o600); err != nil {
		t.Fatal(err)
	}
	list := exec.Command("git", "bundle", "list-heads", bundle)
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("listing bundle: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "refs/heads/main") ||
		!strings.Contains(string(out), "refs/heads/open-proposal") {
		t.Fatalf("bundle does not preserve config refs:\n%s", out)
	}

	restored := filepath.Join(root, "restored")
	clone := exec.Command("git", "clone", bundle, restored)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("cloning bundle: %v: %s", err, out)
	}
	settings, err := os.ReadFile(filepath.Join(restored, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(settings) != "{\"kept\":true}\n" {
		t.Fatalf("settings.json = %q", settings)
	}
	log := exec.Command("git", "log", "--all", "--format=%H", "--", "secrets.enc")
	log.Dir = restored
	if out, err := log.CombinedOutput(); err != nil {
		t.Fatalf("reading restored history: %v: %s", err, out)
	} else if len(bytes.TrimSpace(out)) != 0 {
		t.Fatalf("bundle retained historical secrets.enc: %s", out)
	}
	remote := exec.Command("git", "remote", "get-url", "origin")
	remote.Dir = restored
	if out, err := remote.CombinedOutput(); err != nil {
		t.Fatalf("reading restored remote: %v: %s", err, out)
	} else if strings.Contains(string(out), "token@example") {
		t.Fatalf("bundle retained credential-bearing remote: %s", out)
	}
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

func assertSnapshotRow(t *testing.T, data []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow("SELECT value FROM example").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "kept" {
		t.Fatalf("database value = %q", value)
	}
}
