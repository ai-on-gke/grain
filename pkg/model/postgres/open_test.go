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
	"database/sql"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

func TestOpenConnectsAndVerifies(t *testing.T) {
	t.Parallel()
	db, err := postgres.Open(postgrestest.Config(t))
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("pinging postgres: %v", err)
	}
}

func TestOpenPoolDefaultsAndConnections(t *testing.T) {
	t.Parallel()
	cfg := postgrestest.Config(t)
	cfg.MaxOpenConns = 5
	cfg.MaxIdleConns = 3
	cfg.ConnMaxLifetime = 10 * time.Minute
	cfg.ConnMaxIdleTime = 2 * time.Minute

	db, err := postgres.Open(cfg)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("pinging postgres: %v", err)
	}

	ctx := context.Background()
	const conns = 3
	var held []*sql.Conn
	for i := 0; i < conns; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("taking connection %d from pool: %v", i, err)
		}
		defer c.Close()
		held = append(held, c)
	}

	for i, c := range held {
		var one int
		if err := c.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			t.Fatalf("querying on connection %d: %v", i, err)
		}
		if one != 1 {
			t.Errorf("connection %d: got %d, want 1", i, one)
		}
	}
}

func TestOpenWithSearchPath(t *testing.T) {
	t.Parallel()
	cfg := postgrestest.Config(t)
	cfg.SearchPath = "public"
	db, err := postgres.Open(cfg)
	if err != nil {
		t.Fatalf("opening postgres with search path: %v", err)
	}
	defer db.Close()

	var currentSchema string
	if err := db.QueryRow("SELECT current_schema()").Scan(&currentSchema); err != nil {
		t.Fatalf("reading current schema: %v", err)
	}
	if currentSchema != "public" {
		t.Errorf("got current schema %q, want %q", currentSchema, "public")
	}
}
