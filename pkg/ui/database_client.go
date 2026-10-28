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

package ui

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// DatabaseStatus is what an HTTPClient gets back from GET /api/database.
type DatabaseStatus struct {
	Driver            string `json:"driver"`
	DSN               string `json:"dsn"`
	IsSqlite          bool   `json:"isSqlite"`
	PostgresAvailable bool   `json:"postgresAvailable"`
}

// DatabaseUpgradeResult is what an HTTPClient gets back from POST /api/database/upgrade.
// ConfirmationRequired means nothing was done: the daemon wrote a token
// to its log, to be sent back as DatabaseUpgradeOptions.ConfirmationToken
// before ExpiresAt.
type DatabaseUpgradeResult struct {
	ConfirmationRequired bool      `json:"confirmationRequired"`
	ExpiresAt            time.Time `json:"expiresAt"`
	OK                   bool      `json:"ok"`
	Message              string    `json:"message"`
	TablesMigrated       int       `json:"tablesMigrated"`
	TableNames           []string  `json:"tableNames"`
	Driver               string    `json:"driver"`
	DSN                  string    `json:"dsn"`
	Restarted            bool      `json:"restarted"`
}

// DatabaseUpgradeOptions holds the arguments to UpgradeDatabase.
type DatabaseUpgradeOptions struct {
	DSN         string `json:"dsn"`
	MigrateData bool   `json:"migrateData"`
	Restart     bool   `json:"restart"`
	// ConfirmationToken is the token from the daemon log; empty asks for one.
	ConfirmationToken string `json:"confirmationToken,omitempty"`
}

// DatabaseStatus calls GET /api/database.
func (c *HTTPClient) DatabaseStatus(ctx context.Context) (*DatabaseStatus, error) {
	var res DatabaseStatus
	if err := c.do(ctx, http.MethodGet, "/api/database", nil, &res); err != nil {
		return nil, fmt.Errorf("reading database status: %w", err)
	}
	return &res, nil
}

// UpgradeDatabase calls POST /api/database/upgrade to migrate SQLite to PostgreSQL.
func (c *HTTPClient) UpgradeDatabase(ctx context.Context, opts DatabaseUpgradeOptions) (*DatabaseUpgradeResult, error) {
	var res DatabaseUpgradeResult
	if err := c.do(ctx, http.MethodPost, "/api/database/upgrade", opts, &res); err != nil {
		return nil, fmt.Errorf("upgrading database: %w", err)
	}
	return &res, nil
}
