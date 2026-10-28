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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// DatabaseConfigFile is the filename in dataDir where persistent database backend config is stored.
const DatabaseConfigFile = "db-config.json"

// DatabasePasswordSecret is the secret-store name the password of a
// saved PostgreSQL DSN is kept under, so that db-config.json itself
// never holds it. It is one key, DatabasePasswordKey, so Resolve's
// sole-key form finds it by the bare name.
const (
	DatabasePasswordSecret = "database-password"
	DatabasePasswordKey    = "value"
)

// DatabaseConfig stores the configured database backend driver and DSN.
//
// On disk DSN carries no password: SaveDatabaseConfig moves it into the
// secret store and records the secret's name in PasswordSecret, and
// LoadDatabaseConfig puts it back. A file written before that -- with
// the password still in DSN and no PasswordSecret -- is still read as
// it stands, and MigrateDatabaseConfigPassword moves its password out.
type DatabaseConfig struct {
	Driver         string `json:"driver"`
	DSN            string `json:"dsn"`
	PasswordSecret string `json:"passwordSecret,omitempty"`
}

// PasswordStore is the part of the secret store (pkg/secrets.Store)
// the database config needs: somewhere to put the DSN's password and a
// way to read it back.
type PasswordStore interface {
	Set(secret, key string, value []byte) error
	Resolve(ctx context.Context, name string) (string, error)
}

// UpgradeResult describes the outcome of migrating an SQLite database to PostgreSQL.
type UpgradeResult struct {
	TablesMigrated int           `json:"tablesMigrated"`
	TableNames     []string      `json:"tableNames"`
	Driver         string        `json:"driver"`
	DSN            string        `json:"dsn"`
	Duration       time.Duration `json:"duration"`
}

// SaveDatabaseConfig writes the database configuration to db-config.json
// under dataDir. A password in dsn -- URL userinfo, a ?password= query
// parameter or a key/value password= setting -- is stored in store
// rather than in the file; a dsn that carries one with no store to put
// it in is refused rather than written out in clear text.
func SaveDatabaseConfig(dataDir, driver, dsn string, store PasswordStore) error {
	if dataDir == "" {
		return errors.New("dataDir is required")
	}
	cfg := DatabaseConfig{
		Driver: driver,
		DSN:    dsn,
	}
	if stripped, password, ok := SplitDSNPassword(dsn); ok {
		if store == nil {
			return errors.New("the DSN carries a password and there is no secret store to keep it in; " +
				"refusing to write it to " + DatabaseConfigFile + " in clear text")
		}
		if err := store.Set(DatabasePasswordSecret, DatabasePasswordKey, []byte(password)); err != nil {
			return fmt.Errorf("storing database password in the secret store: %w", err)
		}
		cfg.DSN = stripped
		cfg.PasswordSecret = DatabasePasswordSecret
	}
	return writeDatabaseConfig(dataDir, cfg)
}

func writeDatabaseConfig(dataDir string, cfg DatabaseConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling db config: %w", err)
	}
	target := filepath.Join(dataDir, DatabaseConfigFile)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing db config temp file: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming db config file: %w", err)
	}
	return nil
}

// ReadDatabaseConfig reads db-config.json from dataDir if it exists,
// exactly as it is on disk: DSN is whatever the file holds, which for
// a config written by SaveDatabaseConfig has no password in it. It is
// for a caller that wants the driver, or a DSN to display, and has no
// use for the password.
func ReadDatabaseConfig(dataDir string) (*DatabaseConfig, error) {
	if dataDir == "" {
		return nil, nil
	}
	target := filepath.Join(dataDir, DatabaseConfigFile)
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading db config: %w", err)
	}
	var cfg DatabaseConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing db config %s: %w", target, err)
	}
	return &cfg, nil
}

// LoadDatabaseConfig reads db-config.json from dataDir if it exists and
// returns it with the password, if the config names one, resolved from
// store and put back into DSN: the DSN a connection can be opened with.
// A config that names a password it cannot resolve is an error rather
// than a DSN quietly missing its password.
func LoadDatabaseConfig(ctx context.Context, dataDir string, store PasswordStore) (*DatabaseConfig, error) {
	cfg, err := ReadDatabaseConfig(dataDir)
	if err != nil || cfg == nil || cfg.PasswordSecret == "" {
		return cfg, err
	}
	if store == nil {
		return nil, fmt.Errorf("%s names its password as secret %q, and there is no secret store to read it from",
			DatabaseConfigFile, cfg.PasswordSecret)
	}
	password, err := store.Resolve(ctx, cfg.PasswordSecret)
	if err != nil {
		return nil, fmt.Errorf("resolving the database password from secret %q: %w", cfg.PasswordSecret, err)
	}
	cfg.DSN = JoinDSNPassword(cfg.DSN, password)
	return cfg, nil
}

// HasPlaintextPassword reports whether cfg, as read from disk, still
// carries a password in its DSN: a file written before passwords moved
// into the secret store.
func (cfg *DatabaseConfig) HasPlaintextPassword() bool {
	if cfg == nil {
		return false
	}
	_, _, ok := SplitDSNPassword(cfg.DSN)
	return ok
}

// MigrateDatabaseConfigPassword moves the password out of a
// db-config.json written before SaveDatabaseConfig kept it in the
// secret store, rewriting the file without it. It reports whether it
// rewrote anything; a missing file, or one with no password in it, is
// left alone.
func MigrateDatabaseConfigPassword(dataDir string, store PasswordStore) (bool, error) {
	cfg, err := ReadDatabaseConfig(dataDir)
	if err != nil || !cfg.HasPlaintextPassword() {
		return false, err
	}
	if err := SaveDatabaseConfig(dataDir, cfg.Driver, cfg.DSN, store); err != nil {
		return false, err
	}
	return true, nil
}

// SplitDSNPassword removes the password from a PostgreSQL DSN, in any of
// the places SanitizedDSN masks one: URL userinfo, a URL ?password=
// query parameter, or a key/value DSN's password= setting. ok is false
// when the DSN carries no (or an empty) password, in which case
// stripped is dsn.
func SplitDSNPassword(dsn string) (stripped, password string, ok bool) {
	if !isURLDSN(dsn) {
		loc := reKVPassword.FindStringSubmatchIndex(dsn)
		if loc == nil {
			return dsn, "", false
		}
		password = unquoteKVValue(dsn[loc[4]:loc[5]])
		if password == "" {
			return dsn, "", false
		}
		left := strings.TrimRight(dsn[:loc[0]], " \t")
		right := strings.TrimLeft(dsn[loc[1]:], " \t")
		if left != "" && right != "" {
			return left + " " + right, password, true
		}
		return left + right, password, true
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn, "", false
	}
	if u.User != nil {
		if p, has := u.User.Password(); has {
			password, ok = p, true
			u.User = url.User(u.User.Username())
		}
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err == nil {
			inQuery := false
			for k, vs := range q {
				if strings.EqualFold(k, "password") {
					if !ok && len(vs) > 0 {
						password, ok = vs[0], true
					}
					q.Del(k)
					inQuery = true
				}
			}
			if inQuery {
				u.RawQuery = q.Encode()
			}
		}
	}
	if !ok || password == "" {
		return dsn, "", false
	}
	return u.String(), password, true
}

// JoinDSNPassword puts password back into a DSN SplitDSNPassword
// stripped: as URL userinfo for a URL DSN, as a quoted password=
// setting for a key/value one. An empty password leaves dsn as it is.
func JoinDSNPassword(dsn, password string) string {
	if password == "" {
		return dsn
	}
	if !isURLDSN(dsn) {
		quoted := "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(password) + "'"
		if strings.TrimSpace(dsn) == "" {
			return "password=" + quoted
		}
		return dsn + " password=" + quoted
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func isURLDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// unquoteKVValue undoes libpq's key/value quoting: a value in single
// quotes, with \' and \\ escaping a quote and a backslash.
func unquoteKVValue(v string) string {
	if len(v) < 2 || v[0] != '\'' || v[len(v)-1] != '\'' {
		return v
	}
	v = v[1 : len(v)-1]
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// TestPostgresConnection tests whether a connection can be established with the target PostgreSQL config.
func TestPostgresConnection(ctx context.Context, pgCfg Config) error {
	db, err := Open(pgCfg)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer db.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("pinging postgres: %w", err)
	}
	return nil
}

// UpgradeFromSQLite migrates an existing SQLite database into PostgreSQL.
// It initializes the schema on the target PostgreSQL database, dumps all rows from sqliteDB,
// and imports them into PostgreSQL using staterepo.ImportMigrated.
func UpgradeFromSQLite(ctx context.Context, sqliteDB *sql.DB, pgCfg Config, migrateData bool) (*UpgradeResult, error) {
	if sqliteDB == nil {
		return nil, errors.New("source sqlite database is required")
	}

	start := time.Now()

	// 1. Connect to PostgreSQL
	pgDB, err := Open(pgCfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to target postgres database: %w", err)
	}
	defer pgDB.Close()

	if err := pgDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("pinging target postgres database: %w", err)
	}

	// 2. Initialize schema in PostgreSQL
	pgStore := model.New(pgDB)
	if err := pgStore.Init(ctx); err != nil {
		return nil, fmt.Errorf("initializing postgres schema: %w", err)
	}

	var tableNames []string

	// 3. If migrateData is true, dump and reload into PostgreSQL
	if migrateData {
		tmpDir, err := os.MkdirTemp("", "grain-pg-upgrade-*")
		if err != nil {
			return nil, fmt.Errorf("creating temporary export directory: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		if err := staterepo.Export(ctx, sqliteDB, tmpDir); err != nil {
			return nil, fmt.Errorf("exporting sqlite database: %w", err)
		}

		if err := staterepo.ImportMigrated(ctx, pgDB, tmpDir); err != nil {
			return nil, fmt.Errorf("importing data into postgres database: %w", err)
		}

		if err := syncPostgresSequences(ctx, pgDB); err != nil {
			return nil, err
		}
	}

	// Query tables in the target database to verify
	rows, err := pgDB.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("verifying postgres tables: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning table name: %w", err)
		}
		tableNames = append(tableNames, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading table list: %w", err)
	}

	return &UpgradeResult{
		TablesMigrated: len(tableNames),
		TableNames:     tableNames,
		Driver:         "postgres",
		DSN:            pgCfg.SanitizedDSN(),
		Duration:       time.Since(start),
	}, nil
}

func syncPostgresSequences(ctx context.Context, db *sql.DB) error {
	syncSQL := `
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN (
        SELECT c.table_schema, c.table_name, c.column_name, 
               pg_get_serial_sequence(format('%I.%I', c.table_schema, c.table_name), c.column_name) AS seq_name
        FROM information_schema.columns c
        WHERE c.table_schema = CURRENT_SCHEMA()
          AND pg_get_serial_sequence(format('%I.%I', c.table_schema, c.table_name), c.column_name) IS NOT NULL
    ) LOOP
        EXECUTE format('SELECT setval(%L, COALESCE((SELECT MAX(%I) FROM %I.%I), 1), (SELECT COUNT(*) > 0 FROM %I.%I))', 
                       r.seq_name, r.column_name, r.table_schema, r.table_name, r.table_schema, r.table_name);
    END LOOP;
END $$;`
	if _, err := db.ExecContext(ctx, syncSQL); err != nil {
		return fmt.Errorf("synchronizing postgres sequences: %w", err)
	}
	return nil
}
