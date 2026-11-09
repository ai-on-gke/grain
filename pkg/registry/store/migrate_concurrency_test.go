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
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

// TestConcurrentReplicasMigrateAFreshDatabaseOnce reproduces the 2026-11-07
// registry walkthrough finding: two `grain-registry serve` replicas started
// together against an empty database both applied migration 1, and the loser
// exited with `duplicate key value violates unique constraint
// "pg_type_typname_nsp_index"`. Every replica must now come up cleanly.
func TestConcurrentReplicasMigrateAFreshDatabaseOnce(t *testing.T) {
	baseURL, adminDB, err := ensureSharedPostgres()
	if err != nil {
		if req := os.Getenv(requirePostgresEnv); req != "" && req != "0" && req != "false" {
			t.Fatalf("ensureSharedPostgres: %v", err)
		}
		t.Skipf("skipping postgres test (%v)", err)
	}
	ctx := context.Background()
	dbName := fmt.Sprintf("reg_concurrent_migrate_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + dbName

	const replicas = 6
	dbs := make([]*sql.DB, replicas)
	for i := range dbs {
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		dbs[i] = db
	}
	start := make(chan struct{})
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i, db := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = Migrate(ctx, db)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("replica %d Migrate: %v", i, err)
		}
	}
	var applied, distinct int
	if err := dbs[0].QueryRowContext(ctx, `SELECT count(*), count(DISTINCT version) FROM registry.schema_migrations`).Scan(&applied, &distinct); err != nil {
		t.Fatal(err)
	}
	if applied != len(migrations) || distinct != len(migrations) {
		t.Fatalf("schema_migrations has %d rows (%d distinct), want %d", applied, distinct, len(migrations))
	}
}
