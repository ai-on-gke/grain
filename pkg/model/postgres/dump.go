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
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var reKVSearchPath = regexp.MustCompile(`(?i)\bsearch_path\s*=\s*('(?:[^'\\]|\\.)*'|\S+)`)

// Dump takes an online, custom-format snapshot (`pg_dump --format=custom`) of
// the PostgreSQL store into destination.
//
// When dsnOverride is non-empty it names the target server; otherwise db must
// have been opened by Open so its connection configuration is known. Any
// password in the DSN is stripped from pg_dump's command line and passed via
// PGPASSWORD in an explicit minimal environment instead.
func Dump(ctx context.Context, db *sql.DB, dsnOverride, destination string) error {
	var cfg Config
	if trimmed := strings.TrimSpace(dsnOverride); trimmed != "" {
		cfg = Config{URL: trimmed}
	} else {
		var ok bool
		cfg, ok = ConfigFromDB(db)
		if !ok {
			return errors.New("postgres dump requires a DSN or a *sql.DB opened with postgres.Open")
		}
	}

	connString, password, schema, err := prepareDumpTarget(cfg)
	if err != nil {
		return err
	}

	pgDumpPath, err := findDumpBinary("pg_dump")
	if err != nil {
		return err
	}

	args := []string{
		"--format=custom",
		"--no-password",
		"--file=" + destination,
	}
	if schema != "" && schema != "public" {
		args = append(args, "--schema="+schema)
	}
	args = append(args, "--dbname="+connString)

	cmd := exec.CommandContext(ctx, pgDumpPath, args...)
	env := pgClientEnv()
	if password != "" {
		env = append(env, "PGPASSWORD="+password)
	}
	cmd.Env = env

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if password != "" {
			msg = strings.ReplaceAll(msg, password, "******")
		}
		if msg != "" {
			return fmt.Errorf("pg_dump: %w: %s", err, msg)
		}
		return fmt.Errorf("pg_dump: %w", err)
	}
	return nil
}

// FindBinary locates a PostgreSQL client or server binary (such as pg_dump,
// pg_restore or psql), preferring explicit PATH entries ahead of /usr/bin and
// the newest /usr/lib/postgresql/<major>/bin installation over Debian's
// /usr/bin pg_wrapper.
func FindBinary(name string) (string, error) {
	return findDumpBinary(name)
}

func findDumpBinary(name string) (string, error) {
	lookPath, lookErr := exec.LookPath(name)
	if lookErr == nil && filepath.Clean(lookPath) != filepath.Join("/usr/bin", name) {
		return lookPath, nil
	}

	matches, _ := filepath.Glob("/usr/lib/postgresql/*/bin/" + name)
	type candidate struct {
		major int
		path  string
	}
	var versioned []candidate
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		verDir := filepath.Base(filepath.Dir(filepath.Dir(m)))
		major, _ := strconv.Atoi(verDir)
		versioned = append(versioned, candidate{major: major, path: m})
	}
	slices.SortFunc(versioned, func(a, b candidate) int {
		if a.major != b.major {
			return b.major - a.major
		}
		return strings.Compare(b.path, a.path)
	})
	if len(versioned) > 0 {
		return versioned[0].path, nil
	}

	if lookErr == nil {
		return lookPath, nil
	}
	for _, dir := range []string{"/usr/local/bin", "/usr/bin"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("postgres binary %q not found", name)
}

func pgClientEnv() []string {
	env := pgEnv()
	for _, name := range []string{
		"PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGSSLCRL",
		"PGPASSFILE", "PGSERVICEFILE", "PGSYSCONFDIR",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func prepareDumpTarget(cfg Config) (connString, password, schema string, err error) {
	var dsnSearchPath string
	rawDSN := strings.TrimSpace(cfg.URL)

	if rawDSN == "" {
		rawDSN = buildDSN(cfg)
	}
	if isURLDSN(rawDSN) {
		u, parseErr := url.Parse(rawDSN)
		if parseErr != nil {
			return "", "", "", errors.New("parsing postgres connection URL")
		}
		user := ""
		pass := ""
		hasPass := false
		if u.User != nil {
			user = u.User.Username()
			pass, hasPass = u.User.Password()
		}
		if cfg.User != "" {
			user = cfg.User
		}
		if cfg.Password != "" {
			pass = cfg.Password
			hasPass = true
		}
		if user != "" || hasPass {
			if user == "" {
				user = "postgres"
			}
			if hasPass {
				u.User = url.UserPassword(user, pass)
			} else {
				u.User = url.User(user)
			}
		}

		q, qErr := url.ParseQuery(u.RawQuery)
		if qErr != nil {
			q = url.Values{}
		}
		for k, vs := range q {
			if strings.EqualFold(k, "search_path") {
				if dsnSearchPath == "" && len(vs) > 0 {
					dsnSearchPath = vs[0]
				}
				q.Del(k)
			}
		}
		if cfg.Host != "" {
			if strings.HasPrefix(cfg.Host, "/") {
				u.Host = ""
				q.Set("host", cfg.Host)
				if cfg.Port > 0 && cfg.Port != 5432 {
					q.Set("port", strconv.Itoa(cfg.Port))
				}
			} else {
				port := cfg.Port
				if port <= 0 {
					if _, p, splitErr := net.SplitHostPort(u.Host); splitErr == nil {
						port, _ = strconv.Atoi(p)
					}
				}
				if port > 0 {
					u.Host = net.JoinHostPort(cfg.Host, strconv.Itoa(port))
				} else {
					u.Host = cfg.Host
				}
				q.Del("host")
			}
		} else if cfg.Port > 0 {
			if strings.HasPrefix(q.Get("host"), "/") {
				if cfg.Port != 5432 {
					q.Set("port", strconv.Itoa(cfg.Port))
				}
			} else {
				host := u.Hostname()
				if host == "" {
					host = "localhost"
				}
				u.Host = net.JoinHostPort(host, strconv.Itoa(cfg.Port))
			}
		}
		if cfg.Database != "" {
			u.Path = "/" + cfg.Database
		}
		if cfg.SSLMode != "" {
			q.Set("sslmode", cfg.SSLMode)
		}
		u.RawQuery = q.Encode()
		rawDSN = u.String()
	} else {
		if loc := reKVSearchPath.FindStringSubmatchIndex(rawDSN); loc != nil {
			dsnSearchPath = unquoteKVValue(rawDSN[loc[2]:loc[3]])
			left := strings.TrimRight(rawDSN[:loc[0]], " \t")
			right := strings.TrimLeft(rawDSN[loc[1]:], " \t")
			if left != "" && right != "" {
				rawDSN = left + " " + right
			} else {
				rawDSN = left + right
			}
		}
	}

	stripped, pass, ok := SplitDSNPassword(rawDSN)
	if !ok && cfg.Password != "" {
		pass = cfg.Password
	}

	sp := cfg.SearchPath
	if sp == "" {
		sp = dsnSearchPath
	}
	schema = firstSearchPathEntry(sp)
	return stripped, pass, schema, nil
}
