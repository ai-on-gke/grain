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
	"net/http"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

// database.go is GET /api/database: which store this daemon runs on,
// read-only. There is no route that copies, migrates or switches the
// store any more. A running daemon moving its own store is how the
// bsalmon deployment forked its store twice on 2026-11-04: the copy
// stops at the moment it is taken while the daemon keeps writing to the
// original. A store moves only offline -- the daemon stopped, `grain
// upgrade-postgres` or setup.sh run, the high-water mark checked with
// `grain db check`, the daemon started -- and the pane shows those
// commands (docs/database.md).

type databaseStatusResponse struct {
	Driver            string `json:"driver"`
	DSN               string `json:"dsn"`
	IsSqlite          bool   `json:"isSqlite"`
	PostgresAvailable bool   `json:"postgresAvailable"`
}

func (s *Server) databaseStatus() *databaseStatusResponse {
	driver := s.tasks.Config.DBDriver
	dsn := s.tasks.Config.DBDSN
	if (driver == "" || driver == "sqlite") && s.tasks.Config.DataDir != "" {
		if saved, _ := postgres.ReadDatabaseConfig(s.tasks.Config.DataDir); saved != nil && saved.Driver != "" {
			driver = saved.Driver
			if dsn == "" {
				dsn = saved.DSN
			}
		}
	}
	if driver == "" {
		driver = "sqlite"
	}
	if driver == "postgres" && dsn != "" {
		cfg := postgres.DefaultConfig(dsn)
		dsn = cfg.SanitizedDSN()
	}

	return &databaseStatusResponse{
		Driver:            driver,
		DSN:               dsn,
		IsSqlite:          driver == "sqlite",
		PostgresAvailable: postgres.IsAvailable(),
	}
}

// handleGetDatabase answers GET /api/database: the store's driver and
// (sanitized) DSN, which is the daemon's own plumbing and a system
// administrator's alone to read.
func (s *Server) handleGetDatabase(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.databaseStatus())
}
