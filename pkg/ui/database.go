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
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

type databaseStatusResponse struct {
	Driver            string `json:"driver"`
	DSN               string `json:"dsn"`
	IsSqlite          bool   `json:"isSqlite"`
	PostgresAvailable bool   `json:"postgresAvailable"`
}

type testDatabaseRequest struct {
	DSN string `json:"dsn"`
}

type upgradeDatabaseRequest struct {
	DSN         string `json:"dsn"`
	MigrateData bool   `json:"migrateData"`
	Restart     bool   `json:"restart"`
	// ConfirmationToken is the token the daemon wrote to its own log in
	// answer to an earlier, otherwise identical request. Without it the
	// route only issues one; see databaseConfirmations.
	ConfirmationToken string `json:"confirmationToken"`
}

// upgradeConfirmationResponse is what POST /api/database/upgrade answers
// a request that carries no confirmation token with: nothing has been
// dialed, copied or saved yet.
type upgradeConfirmationResponse struct {
	ConfirmationRequired bool      `json:"confirmationRequired"`
	Message              string    `json:"message"`
	ExpiresAt            time.Time `json:"expiresAt"`
}

// errDatabaseConfirmationInvalid answers a confirmation token that was
// never issued, has expired, was already used, or was issued to another
// principal or for a different DSN or options.
var errDatabaseConfirmationInvalid = apierror.Public("confirmation token is invalid, expired or does not match this request; send the request again without one to be issued a new token")

type upgradeDatabaseResponse struct {
	OK             bool     `json:"ok"`
	Message        string   `json:"message"`
	TablesMigrated int      `json:"tablesMigrated"`
	TableNames     []string `json:"tableNames"`
	Driver         string   `json:"driver"`
	DSN            string   `json:"dsn"`
	Restarted      bool     `json:"restarted"`
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

// errDatabaseConnectionFailed is all handleTestDatabaseConnection tells
// its caller about a failed connection. The driver's own error names the
// dial address and tells "connection refused" from "unexpected EOF" from
// a timeout, which turns this route into a port scanner for whatever the
// daemon's host can reach; that detail goes to the daemon's log instead.
var errDatabaseConnectionFailed = apierror.Public("could not connect to PostgreSQL with the supplied settings; see the daemon log for details")

// handleTestDatabaseConnection answers POST /api/database/test. It dials
// whatever DSN it is given, so only a system administrator may call it.
func (s *Server) handleTestDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	var req testDatabaseRequest
	if !readJSON(w, r, &req) {
		return
	}
	dsn := strings.TrimSpace(req.DSN)
	if dsn == "" {
		if !postgres.IsAvailable() {
			writeError(w, http.StatusBadRequest, errors.New("dsn is required (no embedded postgres found)"))
			return
		}
	}

	var pgCfg postgres.Config
	if dsn != "" {
		pgCfg = postgres.DefaultConfig(dsn)
	} else {
		cfg, stop, err := postgres.EnsureRunning(r.Context(), s.tasks.Config.DataDir)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer stop()
		pgCfg = cfg
	}

	if err := postgres.TestPostgresConnection(r.Context(), pgCfg); err != nil {
		log.Printf("ui: testing database connection to %s: %v", pgCfg.SanitizedDSN(), err)
		writeError(w, http.StatusBadRequest, errDatabaseConnectionFailed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "PostgreSQL connection successful"})
}

// handleUpgradeDatabase answers POST /api/database/upgrade and its
// aliases. It copies the whole store to the target DSN and repoints the
// daemon at it, so only a system administrator may call it, and only
// with a confirmation token the daemon wrote to its log in answer to an
// earlier request for exactly the same thing (databaseConfirmations).
func (s *Server) handleUpgradeDatabase(w http.ResponseWriter, r *http.Request) {
	var req upgradeDatabaseRequest
	req.MigrateData = true
	if r.Body != nil && r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}

	dsn := strings.TrimSpace(req.DSN)
	if dsn == "" && !postgres.IsAvailable() {
		writeError(w, http.StatusBadRequest, errors.New("postgres DSN is required"))
		return
	}

	principal := s.tasks.actor(r.Context()).ID
	binding := databaseConfirmBinding(principal, dsn, req.MigrateData, req.Restart)
	if strings.TrimSpace(req.ConfirmationToken) == "" {
		token, expires, err := s.dbConfirm.issue(binding, s.tasks.now())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		target := "the embedded PostgreSQL"
		if dsn != "" {
			target = postgres.DefaultConfig(dsn).SanitizedDSN()
		}
		s.announceDatabaseConfirmation(token, fmt.Sprintf(
			"database upgrade requested by %q: copy the store to %s and make it this daemon's database (migrateData=%t, restart=%t); confirmation token expires at %s",
			principal, target, req.MigrateData, req.Restart, expires.UTC().Format(time.RFC3339)))
		writeJSON(w, http.StatusAccepted, upgradeConfirmationResponse{
			ConfirmationRequired: true,
			Message:              "Nothing has been changed yet. A confirmation token has been written to the daemon log; send this same request again with it as confirmationToken to proceed.",
			ExpiresAt:            expires,
		})
		return
	}
	if !s.dbConfirm.redeem(req.ConfirmationToken, binding, s.tasks.now()) {
		writeError(w, http.StatusForbidden, errDatabaseConfirmationInvalid)
		return
	}

	var pgCfg postgres.Config
	if dsn != "" {
		pgCfg = postgres.DefaultConfig(dsn)
	} else {
		cfg, _, err := postgres.EnsureRunning(r.Context(), s.tasks.Config.DataDir)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		pgCfg = cfg
	}

	var res *postgres.UpgradeResult
	if s.tasks.Config.UpgradePostgres != nil {
		var err error
		res, err = s.tasks.Config.UpgradePostgres(r.Context(), dsn, req.MigrateData)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	} else {
		if s.tasks.Config.SourceDB == nil {
			writeError(w, http.StatusNotFound, errors.New("database upgrade is not available on this server: source DB is not configured"))
			return
		}
		var err error
		res, err = postgres.UpgradeFromSQLite(r.Context(), s.tasks.Config.SourceDB, pgCfg, req.MigrateData)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	if s.tasks.Config.DataDir != "" {
		targetDSN := dsn
		if targetDSN == "" {
			targetDSN = pgCfg.URL
		}
		// Without this file the daemon reopens SQLite at its next start
		// and everything written to PostgreSQL in between is out of sight,
		// so a failure here is the upgrade's failure: it is reported, and
		// the daemon is neither repointed nor restarted.
		// The DSN's password goes to the secret store, not the file.
		var passwords postgres.PasswordStore
		if s.tasks.Config.Secrets != nil {
			passwords = s.tasks.Config.Secrets
		}
		if err := postgres.SaveDatabaseConfig(s.tasks.Config.DataDir, "postgres", targetDSN, passwords); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf(
				"the data was copied to PostgreSQL, but recording the switch failed, so grain would reopen SQLite at its next start: %w", err))
			return
		}
	}

	s.tasks.Config.DBDriver = "postgres"
	if dsn != "" {
		s.tasks.Config.DBDSN = dsn
	} else if pgCfg.URL != "" {
		s.tasks.Config.DBDSN = pgCfg.URL
	}

	restarted := false
	if req.Restart && s.tasks.Config.Restart != nil {
		if err := s.tasks.Config.Restart(r.Context()); err == nil {
			restarted = true
		}
	}

	msg := "Successfully upgraded database to PostgreSQL"
	if res.TablesMigrated > 0 {
		msg = fmt.Sprintf("Successfully migrated SQLite database to PostgreSQL (%d tables migrated)", res.TablesMigrated)
	}

	writeJSON(w, http.StatusOK, upgradeDatabaseResponse{
		OK:             true,
		Message:        msg,
		TablesMigrated: res.TablesMigrated,
		TableNames:     res.TableNames,
		Driver:         "postgres",
		DSN:            res.DSN,
		Restarted:      restarted,
	})
}
