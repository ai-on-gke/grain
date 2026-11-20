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
	"context"
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

const (
	// DefaultMaxOpenConns is the default PostgreSQL pool ceiling when neither
	// Config.MaxOpenConns nor a DSN pool_max_conns parameter sets one. It leaves
	// headroom beneath PostgreSQL's stock max_connections=100 for the writer
	// lock session, CLI tools, backups, and rolling-upgrade peers while giving
	// concurrent readers and writers room to run without starving on the pool.
	DefaultMaxOpenConns = 50
	// DefaultMaxIdleConns matches DefaultMaxOpenConns so bursts reuse warm
	// backends and their prepared-statement caches instead of churning
	// connections; ConnMaxIdleTime still reaps idle connections when quiet.
	DefaultMaxIdleConns = DefaultMaxOpenConns
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

	// Connection pool settings. Defaults are applied if left zero (after any
	// pool_max_conns / pool_max_idle_conns / pool_max_conn_lifetime /
	// pool_max_conn_idle_time parameters present in URL).
	MaxOpenConns    int           // default: DefaultMaxOpenConns (50)
	MaxIdleConns    int           // default: DefaultMaxIdleConns (25)
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
	explicitPool := cfg.MaxOpenConns > 0
	var dsnPool dsnPoolSettings
	cfg.URL, dsnPool = extractDSNPoolSettings(cfg.URL)
	if cfg.MaxOpenConns <= 0 && dsnPool.maxOpenConns > 0 {
		cfg.MaxOpenConns = dsnPool.maxOpenConns
		explicitPool = true
	}
	if cfg.MaxIdleConns <= 0 && dsnPool.maxIdleConns > 0 {
		cfg.MaxIdleConns = dsnPool.maxIdleConns
	}
	if cfg.ConnMaxLifetime <= 0 && dsnPool.connMaxLifetime > 0 {
		cfg.ConnMaxLifetime = dsnPool.connMaxLifetime
	}
	if cfg.ConnMaxIdleTime <= 0 && dsnPool.connMaxIdleTime > 0 {
		cfg.ConnMaxIdleTime = dsnPool.connMaxIdleTime
	}

	connConfig, err := buildConnConfig(cfg)
	if err != nil {
		return nil, err
	}

	rawConnector := stdlib.GetConnector(*connConfig)
	wrappedConnector := newAdaptedConnector(rawConnector, cfg, explicitPool)
	db := sql.OpenDB(wrappedConnector)

	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = DefaultMaxOpenConns
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = min(maxOpen, DefaultMaxIdleConns)
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

func buildConnConfig(cfg Config) (*pgx.ConnConfig, error) {
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

	return connConfig, nil
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

// ConfigFromDB returns the Config a *sql.DB was opened with when it was
// created by Open.
func ConfigFromDB(db *sql.DB) (Config, bool) {
	if db == nil {
		return Config{}, false
	}
	d, ok := db.Driver().(*adaptedDriver)
	if !ok {
		return Config{}, false
	}
	return d.cfg, true
}

// SizePoolForWorkers sizes db's connection pool for a grain daemon running up
// to maxWorkers concurrent runs, capped safely below the PostgreSQL server's
// max_connections so admin sessions, the writer lock, backups, and a rolling
// upgrade peer always have room.
//
// It is a no-op when db is not opened by Open or when the caller explicitly
// configured MaxOpenConns (either on Config or via pool_max_conns in the DSN).
func SizePoolForWorkers(ctx context.Context, db *sql.DB, maxWorkers int) (maxOpen, maxIdle int) {
	if db == nil {
		return 0, 0
	}
	d, ok := db.Driver().(*adaptedDriver)
	if !ok {
		return 0, 0
	}
	if d.explicitPool {
		stats := db.Stats()
		return stats.MaxOpenConnections, 0
	}

	serverMax := 100
	var raw string
	if err := db.QueryRowContext(ctx, "SHOW max_connections").Scan(&raw); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
			serverMax = n
		}
	}

	maxOpen, maxIdle = poolSizeForDaemon(maxWorkers, serverMax)
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	return maxOpen, maxIdle
}

// poolSizeForDaemon computes the daemon's pool limits from its worker count
// and the server's max_connections. It leaves at least 20% (minimum 10) of
// serverMax unallocated for the dedicated writer-lock session, superuser
// reserve, backups, CLI tools, and rolling-upgrade overlap.
func poolSizeForDaemon(maxWorkers, serverMax int) (maxOpen, maxIdle int) {
	reserve := max(10, serverMax/5)
	serverCap := max(10, serverMax-reserve)

	wantOpen := DefaultMaxOpenConns
	if maxWorkers > 0 {
		// Reserve headroom for API handlers, orchestrator, dispatcher,
		// change-token probe, and concurrent live-run heartbeats/writes.
		wantOpen = max(DefaultMaxOpenConns, maxWorkers+25)
	}
	maxOpen = min(wantOpen, serverCap)
	maxIdle = maxOpen
	return maxOpen, maxIdle
}

type dsnPoolSettings struct {
	maxOpenConns    int
	maxIdleConns    int
	connMaxLifetime time.Duration
	connMaxIdleTime time.Duration
}

// extractDSNPoolSettings parses and strips pgxpool-style URL parameters from
// rawURL so pgx.ParseConfig does not forward them to PostgreSQL as unknown
// RuntimeParams. Non-URL DSNs are returned unchanged.
func extractDSNPoolSettings(rawURL string) (string, dsnPoolSettings) {
	var out dsnPoolSettings
	if rawURL == "" || (!strings.HasPrefix(rawURL, "postgres://") && !strings.HasPrefix(rawURL, "postgresql://")) {
		return rawURL, out
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, out
	}
	q := u.Query()
	changed := false
	if v := strings.TrimSpace(q.Get("pool_max_conns")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.maxOpenConns = n
		}
		q.Del("pool_max_conns")
		changed = true
	}
	if v := strings.TrimSpace(q.Get("pool_max_idle_conns")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.maxIdleConns = n
		}
		q.Del("pool_max_idle_conns")
		changed = true
	}
	if v := strings.TrimSpace(q.Get("pool_min_conns")); v != "" {
		if out.maxIdleConns == 0 {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				out.maxIdleConns = n
			}
		}
		q.Del("pool_min_conns")
		changed = true
	}
	if v := strings.TrimSpace(q.Get("pool_max_conn_lifetime")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			out.connMaxLifetime = d
		}
		q.Del("pool_max_conn_lifetime")
		changed = true
	}
	if v := strings.TrimSpace(q.Get("pool_max_conn_idle_time")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			out.connMaxIdleTime = d
		}
		q.Del("pool_max_conn_idle_time")
		changed = true
	}
	if !changed {
		return rawURL, out
	}
	u.RawQuery = q.Encode()
	return u.String(), out
}
