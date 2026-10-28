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

// Package postgres connects to a PostgreSQL database and sets up the
// connection pool and dialect adapter for model.Store.
//
// This is the only package that imports the PostgreSQL driver
// (github.com/jackc/pgx/v5/stdlib). Everything above it takes a *sql.DB,
// so model.Store remains testable across engines without carrying this
// driver dependency tree.
package postgres

import (
	"crypto/tls"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Config describes how to connect to a PostgreSQL database.
type Config struct {
	// URL is a PostgreSQL connection URL or DSN (e.g.,
	// "postgres://user:password@host:5432/dbname?sslmode=disable").
	// When URL is provided, individual connection fields are parsed from it
	// and can be overridden by explicit struct fields.
	URL string

	// Host specifies the hostname, IP address, or absolute path to a Unix
	// domain socket directory (e.g. "/cloudsql/PROJECT:REGION:INSTANCE" or
	// "/var/run/postgresql").
	Host string

	// Port specifies the TCP port (defaults to 5432).
	Port int

	// Database names the database.
	Database string

	// User names the authenticating role.
	User string

	// Password provides the password for authentication.
	Password string

	// SSLMode specifies TLS behavior ("disable", "require", "verify-ca", "verify-full").
	// Defaults to "disable" for localhost and Unix sockets, and "require" otherwise.
	SSLMode string

	// SearchPath sets the schema search path for connections in the pool.
	SearchPath string

	// Connection pool settings. Defaults are applied if left zero.
	MaxOpenConns    int           // default: 25
	MaxIdleConns    int           // default: 10
	ConnMaxLifetime time.Duration // default: 30 * time.Minute
	ConnMaxIdleTime time.Duration // default: 5 * time.Minute

	// Network timeouts and keepalive settings.
	KeepAlive      time.Duration // default: 1 * time.Minute
	ConnectTimeout time.Duration // default: 10 * time.Second
}

// DefaultConfig returns a Config with default connection pool settings
// pointing to the specified database URL.
func DefaultConfig(connURL string) Config {
	return Config{
		URL:             connURL,
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: 30 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
		KeepAlive:       1 * time.Minute,
		ConnectTimeout:  10 * time.Second,
	}
}

// Open connects to a PostgreSQL database using pgx/v5/stdlib, configures
// the connection pool and keepalive settings, wraps the driver with
// query/dialect adaptation for model.Store, and verifies connectivity.
func Open(cfg Config) (*sql.DB, error) {
	connConfig, err := parseConnConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: parsing config: %w", err)
	}

	keepAlive := cfg.KeepAlive
	if keepAlive <= 0 {
		keepAlive = 1 * time.Minute
	}
	connectTimeout := cfg.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}

	dialer := &net.Dialer{
		KeepAlive: keepAlive,
		Timeout:   connectTimeout,
	}
	connConfig.DialFunc = dialer.DialContext

	if cfg.SearchPath != "" {
		if connConfig.RuntimeParams == nil {
			connConfig.RuntimeParams = make(map[string]string)
		}
		connConfig.RuntimeParams["search_path"] = cfg.SearchPath
	}

	rawConnector := stdlib.GetConnector(*connConfig)
	wrappedConnector := newAdaptedConnector(rawConnector)
	db := sql.OpenDB(wrappedConnector)

	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 25
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 10
	}
	connMaxLifetime := cfg.ConnMaxLifetime
	if connMaxLifetime <= 0 {
		connMaxLifetime = 30 * time.Minute
	}
	connMaxIdleTime := cfg.ConnMaxIdleTime
	if connMaxIdleTime <= 0 {
		connMaxIdleTime = 5 * time.Minute
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetConnMaxIdleTime(connMaxIdleTime)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: connecting: %w", err)
	}

	_, _ = db.Exec(`CREATE OR REPLACE VIEW "sqlite_master" AS
SELECT
  CASE WHEN table_type = 'VIEW' THEN 'view' ELSE 'table' END::text AS "type",
  table_name::text AS "name",
  table_name::text AS "tbl_name",
  0::int AS "rootpage",
  ''::text AS "sql"
FROM information_schema.tables
WHERE table_schema = current_schema()
UNION ALL
SELECT
  'index'::text AS "type",
  indexname::text AS "name",
  tablename::text AS "tbl_name",
  0::int AS "rootpage",
  ''::text AS "sql"
FROM pg_indexes
WHERE schemaname = current_schema()`)

	return db, nil
}

func parseConnConfig(cfg Config) (*pgx.ConnConfig, error) {
	var connString string
	if cfg.URL != "" {
		connString = cfg.URL
		if u, err := url.Parse(connString); err == nil && u.User == nil {
			user := cfg.User
			if user == "" {
				user = "postgres"
			}
			if cfg.Password != "" {
				u.User = url.UserPassword(user, cfg.Password)
			} else {
				u.User = url.User(user)
			}
			connString = u.String()
		}
	} else {
		connString = buildDSN(cfg)
	}

	connConfig, err := pgx.ParseConfig(connString)
	if err != nil {
		return nil, err
	}

	// If explicit host is provided, or Cloud SQL socket directory is configured:
	if cfg.Host != "" {
		connConfig.Host = cfg.Host
	}
	if cfg.Port > 0 {
		connConfig.Port = uint16(cfg.Port)
	}
	if cfg.Database != "" {
		connConfig.Database = cfg.Database
	}
	if cfg.User != "" {
		connConfig.User = cfg.User
	}
	if cfg.Password != "" {
		connConfig.Password = cfg.Password
	}
	if cfg.SSLMode != "" {
		switch strings.ToLower(cfg.SSLMode) {
		case "disable":
			connConfig.TLSConfig = nil
		case "require", "verify-ca", "verify-full":
			if connConfig.TLSConfig == nil {
				connConfig.TLSConfig = &tls.Config{
					InsecureSkipVerify: strings.ToLower(cfg.SSLMode) == "require",
				}
			}
		}
	}

	return connConfig, nil
}

func buildDSN(cfg Config) string {
	u := &url.URL{
		Scheme: "postgres",
	}

	user := cfg.User
	if user == "" {
		user = "postgres"
	}
	if cfg.Password != "" {
		u.User = url.UserPassword(user, cfg.Password)
	} else {
		u.User = url.User(user)
	}

	host := cfg.Host
	if host == "" {
		host = "localhost"
	}

	// Unix domain socket check (e.g. /cloudsql/... or /var/run/postgresql)
	if strings.HasPrefix(host, "/") {
		u.Host = ""
		q := url.Values{}
		q.Set("host", host)
		if cfg.SSLMode != "" {
			q.Set("sslmode", cfg.SSLMode)
		} else {
			q.Set("sslmode", "disable")
		}
		if cfg.Database != "" {
			u.Path = "/" + cfg.Database
		}
		u.RawQuery = q.Encode()
		return u.String()
	}

	port := cfg.Port
	if port <= 0 {
		port = 5432
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))

	if cfg.Database != "" {
		u.Path = "/" + cfg.Database
	}

	q := url.Values{}
	if cfg.SSLMode != "" {
		q.Set("sslmode", cfg.SSLMode)
	} else if host == "localhost" || host == "127.0.0.1" {
		q.Set("sslmode", "disable")
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}

	return u.String()
}

var (
	// reKVPassword matches the password in a key/value DSN
	// ("host=db password=secret"), quoted or bare.
	reKVPassword = regexp.MustCompile(`(?i)(\bpassword\s*=\s*)('(?:[^'\\]|\\.)*'|\S+)`)
	// reQueryPassword matches a password passed as a URL query parameter
	// ("postgres://u@h/db?password=secret").
	reQueryPassword = regexp.MustCompile(`(?i)((?:^|&)password=)[^&]*`)
)

// SanitizedDSN returns the connection DSN with the password masked,
// whether it is given as URL userinfo, as a URL query parameter, or as a
// key/value DSN's password= setting.
func (cfg Config) SanitizedDSN() string {
	raw := cfg.URL
	if raw == "" {
		raw = buildDSN(cfg)
	}
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		return reKVPassword.ReplaceAllString(raw, "${1}******")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return cfg.Host
	}
	if u.User != nil {
		if _, hasPass := u.User.Password(); hasPass {
			u.User = url.UserPassword(u.User.Username(), "******")
		}
	}
	u.RawQuery = reQueryPassword.ReplaceAllString(u.RawQuery, "${1}******")
	return u.String()
}
