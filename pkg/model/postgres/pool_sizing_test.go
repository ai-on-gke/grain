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

package postgres

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"
)

func TestPoolSizeForDaemon(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		maxWorkers int
		serverMax  int
		wantOpen   int
		wantIdle   int
	}{
		{"default workers on stock 100", 0, 100, DefaultMaxOpenConns, DefaultMaxOpenConns},
		{"small workers on stock 100", 8, 100, DefaultMaxOpenConns, DefaultMaxOpenConns},
		{"100 workers capped by stock 100", 100, 100, 80, 80},
		{"100 workers on embedded 200", 100, 200, 125, 125},
		{"200 workers capped by embedded 200", 200, 200, 160, 160},
		{"100 workers on large 500", 100, 500, 125, 125},
		{"tiny serverMax 20", 10, 20, 10, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOpen, gotIdle := poolSizeForDaemon(tc.maxWorkers, tc.serverMax)
			if gotOpen != tc.wantOpen || gotIdle != tc.wantIdle {
				t.Fatalf("poolSizeForDaemon(%d, %d) = (%d, %d), want (%d, %d)",
					tc.maxWorkers, tc.serverMax, gotOpen, gotIdle, tc.wantOpen, tc.wantIdle)
			}
		})
	}
}

func TestExtractDSNPoolSettings(t *testing.T) {
	t.Parallel()
	raw := "postgres://u:p@localhost:5432/grain?sslmode=disable&pool_max_conns=42&pool_max_idle_conns=18&pool_max_conn_lifetime=15m&pool_max_conn_idle_time=2m"
	cleaned, got := extractDSNPoolSettings(raw)
	if got.maxOpenConns != 42 || got.maxIdleConns != 18 || got.connMaxLifetime != 15*time.Minute || got.connMaxIdleTime != 2*time.Minute {
		t.Fatalf("extractDSNPoolSettings = %+v, want 42/18/15m/2m", got)
	}
	u, err := url.Parse(cleaned)
	if err != nil {
		t.Fatalf("parsing cleaned URL %q: %v", cleaned, err)
	}
	for _, key := range []string{"pool_max_conns", "pool_max_idle_conns", "pool_max_conn_lifetime", "pool_max_conn_idle_time"} {
		if u.Query().Has(key) {
			t.Errorf("cleaned URL still contains %q: %s", key, cleaned)
		}
	}
	if u.Query().Get("sslmode") != "disable" {
		t.Errorf("cleaned URL lost sslmode=disable: %s", cleaned)
	}

	// pool_min_conns acts as fallback for maxIdleConns when pool_max_idle_conns is absent.
	_, minOnly := extractDSNPoolSettings("postgresql://u@localhost/grain?pool_min_conns=7")
	if minOnly.maxIdleConns != 7 {
		t.Errorf("pool_min_conns fallback = %d, want 7", minOnly.maxIdleConns)
	}
}

func TestOpenDSNPoolParamsAndSizePoolForWorkers(t *testing.T) {
	if !IsAvailable() {
		t.Skip("postgres binaries not available")
	}
	ctx := context.Background()
	baseCfg, stop, err := EnsureRunning(ctx, t.TempDir(), WithoutCrashSafety())
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer func() { _ = stop() }()

	// DefaultConfig opens at DefaultMaxOpenConns and can be resized by SizePoolForWorkers.
	dbDefault, err := Open(baseCfg)
	if err != nil {
		t.Fatalf("Open(default): %v", err)
	}
	defer dbDefault.Close()
	if got := dbDefault.Stats().MaxOpenConnections; got != DefaultMaxOpenConns {
		t.Fatalf("default MaxOpenConnections = %d, want %d", got, DefaultMaxOpenConns)
	}
	maxOpen, maxIdle := SizePoolForWorkers(ctx, dbDefault, 100)
	if maxOpen != 125 || maxIdle != 125 {
		t.Fatalf("SizePoolForWorkers(100) = (%d, %d), want (125, 125) on embedded max_connections=200", maxOpen, maxIdle)
	}
	if got := dbDefault.Stats().MaxOpenConnections; got != 125 {
		t.Fatalf("after SizePoolForWorkers MaxOpenConnections = %d, want 125", got)
	}

	// DSN pool_max_conns is accepted without Postgres rejecting it as an unknown GUC
	// and locks the pool against automatic resizing.
	dsn := buildDSN(baseCfg)
	if baseCfg.Port > 0 {
		dsn += fmt.Sprintf("&port=%d", baseCfg.Port)
	}
	dsn += "&pool_max_conns=17&pool_max_idle_conns=9&pool_max_conn_idle_time=45s"
	dbDSN, err := Open(DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("Open(DSN with pool_max_conns): %v", err)
	}
	defer dbDSN.Close()
	if got := dbDSN.Stats().MaxOpenConnections; got != 17 {
		t.Fatalf("DSN MaxOpenConnections = %d, want 17", got)
	}
	resizedOpen, _ := SizePoolForWorkers(ctx, dbDSN, 100)
	if resizedOpen != 17 || dbDSN.Stats().MaxOpenConnections != 17 {
		t.Fatalf("SizePoolForWorkers overrode explicit DSN pool_max_conns: got %d (stats %d), want 17",
			resizedOpen, dbDSN.Stats().MaxOpenConnections)
	}
}
