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

package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const requirePostgresEnv = "GRAIN_REQUIRE_POSTGRES"

var (
	sharedPGOnce sync.Once
	sharedPGDir  string
	sharedPGURL  string
	sharedPGDB   *sql.DB
	sharedPGErr  error
	dbSeqMu      sync.Mutex
	dbSeq        int
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopSharedPostgres()
	os.Exit(code)
}

func findPGBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	matches, _ := filepath.Glob(filepath.Join("/usr/lib/postgresql", "*", "bin", name))
	if len(matches) > 0 {
		sort.Strings(matches)
		return matches[len(matches)-1]
	}
	return ""
}

func ensureSharedPostgres() (string, *sql.DB, error) {
	sharedPGOnce.Do(func() {
		if envURL := strings.TrimSpace(os.Getenv("GRAIN_TEST_POSTGRES_URL")); envURL != "" {
			db, err := sql.Open("pgx", envURL)
			if err != nil {
				sharedPGErr = err
				return
			}
			sharedPGURL = envURL
			sharedPGDB = db
			return
		}

		initdbBin := findPGBin("initdb")
		pgCtlBin := findPGBin("pg_ctl")
		if initdbBin == "" || pgCtlBin == "" {
			sharedPGErr = fmt.Errorf("postgresql binaries (initdb/pg_ctl) not found in PATH or /usr/lib/postgresql/*/bin")
			return
		}

		dir, err := os.MkdirTemp("/tmp", "grain-reg-pg-*")
		if err != nil {
			sharedPGErr = err
			return
		}
		sharedPGDir = dir
		dataDir := filepath.Join(dir, "data")
		sockDir := filepath.Join(dir, "sock")
		if err := os.MkdirAll(sockDir, 0o755); err != nil {
			sharedPGErr = err
			return
		}

		asPostgresUser := os.Geteuid() == 0
		if asPostgresUser {
			if pgUser, err := user.Lookup("postgres"); err == nil {
				_ = exec.Command("chown", "-R", pgUser.Username, dir).Run()
			} else {
				asPostgresUser = false
			}
		}

		runCmd := func(bin string, args ...string) error {
			var cmd *exec.Cmd
			if asPostgresUser {
				all := append([]string{"-u", "postgres", "--", bin}, args...)
				cmd = exec.Command("runuser", all...)
			} else {
				cmd = exec.Command(bin, args...)
			}
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%s %v: %w (%s)", bin, args, err, strings.TrimSpace(out.String()))
			}
			return nil
		}

		if err := runCmd(initdbBin, "-D", dataDir, "-U", "postgres", "-A", "trust", "--no-instructions", "--encoding=UTF8", "--locale=C"); err != nil {
			sharedPGErr = err
			return
		}
		pgOpts := fmt.Sprintf("-F -c listen_addresses= -c unix_socket_directories=%s -c max_connections=100", sockDir)
		if err := runCmd(pgCtlBin, "-D", dataDir, "-l", filepath.Join(dir, "postgres.log"), "-o", pgOpts, "-w", "start"); err != nil {
			sharedPGErr = err
			return
		}

		q := make(url.Values)
		q.Set("host", sockDir)
		q.Set("sslmode", "disable")
		u := &url.URL{
			Scheme:   "postgres",
			User:     url.User("postgres"),
			Host:     "localhost",
			Path:     "/postgres",
			RawQuery: q.Encode(),
		}
		sharedPGURL = u.String()
		db, err := sql.Open("pgx", sharedPGURL)
		if err != nil {
			sharedPGErr = err
			return
		}
		sharedPGDB = db
	})
	return sharedPGURL, sharedPGDB, sharedPGErr
}

func stopSharedPostgres() {
	if sharedPGDB != nil {
		_ = sharedPGDB.Close()
	}
	if sharedPGDir == "" {
		return
	}
	pgCtlBin := findPGBin("pg_ctl")
	if pgCtlBin != "" {
		dataDir := filepath.Join(sharedPGDir, "data")
		if os.Geteuid() == 0 {
			_ = exec.Command("runuser", "-u", "postgres", "--", pgCtlBin, "-D", dataDir, "-m", "immediate", "-w", "stop").Run()
		} else {
			_ = exec.Command(pgCtlBin, "-D", dataDir, "-m", "immediate", "-w", "stop").Run()
		}
	}
	_ = os.RemoveAll(sharedPGDir)
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	baseURL, adminDB, err := ensureSharedPostgres()
	if err != nil {
		req := os.Getenv(requirePostgresEnv)
		if req != "" && req != "0" && req != "false" {
			t.Fatalf("ensureSharedPostgres: %v", err)
		}
		t.Skipf("skipping postgres test (%v)", err)
	}

	dbSeqMu.Lock()
	dbSeq++
	seq := dbSeq
	dbSeqMu.Unlock()

	dbName := fmt.Sprintf("reg_test_%d_%d", time.Now().UnixNano(), seq)
	ctx := context.Background()
	if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s`, dbName)); err != nil {
		t.Fatalf("CREATE DATABASE %s: %v", dbName, err)
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	u.Path = "/" + dbName

	st, err := Open(ctx, u.String(), DefaultThresholds())
	if err != nil {
		t.Fatalf("Open(%s): %v", dbName, err)
	}
	t.Cleanup(func() {
		_ = st.Close()
		_, _ = adminDB.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, dbName))
	})
	return st
}
