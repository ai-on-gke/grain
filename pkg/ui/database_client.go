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
)

// DatabaseStatus is what an HTTPClient gets back from GET /api/database.
type DatabaseStatus struct {
	Driver            string `json:"driver"`
	DSN               string `json:"dsn"`
	IsSqlite          bool   `json:"isSqlite"`
	PostgresAvailable bool   `json:"postgresAvailable"`
}

// DatabaseStatus calls GET /api/database.
func (c *HTTPClient) DatabaseStatus(ctx context.Context) (*DatabaseStatus, error) {
	var res DatabaseStatus
	if err := c.do(ctx, http.MethodGet, "/api/database", nil, &res); err != nil {
		return nil, fmt.Errorf("reading database status: %w", err)
	}
	return &res, nil
}
