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
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/backup"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

type dummyDestination struct{}

func (d dummyDestination) Upload(ctx context.Context, name, mediaType string, content io.Reader) (backup.Remote, error) {
	return backup.Remote{}, nil
}

func (d dummyDestination) String() string {
	return "dummy"
}

func TestBackupRefusesDatabaseSnapshotOnPostgres(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	ctx := context.Background()
	_, db := postgrestest.Open(t)

	// Create a dummy git repository for configRepo
	configRepoDir := t.TempDir()
	initCmd := exec.Command("git", "init", "-b", "main")
	initCmd.Dir = configRepoDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// Configure dummy git user
	_ = exec.Command("git", "-C", configRepoDir, "config", "user.name", "test").Run()
	_ = exec.Command("git", "-C", configRepoDir, "config", "user.email", "test@example.com").Run()
	dummyFile := filepath.Join(configRepoDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", configRepoDir, "add", ".").Run()
	_ = exec.Command("git", "-C", configRepoDir, "commit", "-m", "init").Run()

	svc := backup.Service{
		Destination: dummyDestination{},
	}

	_, err := svc.Run(ctx, db, configRepoDir)
	if err == nil {
		t.Fatalf("expected error running backup on Postgres, got nil")
	}
	if !errors.Is(err, backup.ErrPostgresNotSupported) {
		t.Fatalf("expected ErrPostgresNotSupported, got: %v", err)
	}
}
