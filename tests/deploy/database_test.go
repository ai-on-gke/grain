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

package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The database block of setup.sh: a fresh install provisions a local
// PostgreSQL server, an existing one keeps the store it has. These run the
// block itself under bash, with docker and systemctl stubbed to record what
// they were asked, against a scratch data directory and unit paths.

const (
	databaseBegin = "# --- the database (GRAIN_DB_DRIVER"
	databaseEnd   = "# --- end of the database"
)

// dbHost is one host the block runs against: a data directory, where its
// units and DSN file go, and the log of docker/systemctl calls.
type dbHost struct {
	data, unit, postgresUnit, envFile, calls string
}

func newDBHost(t *testing.T) dbHost {
	t.Helper()
	root := t.TempDir()
	h := dbHost{
		data:         filepath.Join(root, "data"),
		unit:         filepath.Join(root, "systemd", "grain-daemon.service"),
		postgresUnit: filepath.Join(root, "systemd", "grain-postgres.service"),
		envFile:      filepath.Join(root, "etc-grain", "postgres.env"),
		calls:        filepath.Join(root, "calls"),
	}
	for _, d := range []string{filepath.Join(h.data, "secrets"), filepath.Dir(h.unit)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// run sources the block with env set and runs steps after it, returning
// what it printed and whether it succeeded.
func (h dbHost) run(t *testing.T, steps string, env ...string) (string, bool) {
	t.Helper()
	text := setupText(t)
	block := between(t, text, databaseBegin, databaseEnd)
	lib := filepath.Join(t.TempDir(), "database.sh")
	stubs := `
log() { echo "==> $*"; }
step_start() { echo "==> $*"; }
step_done() { echo "    done: $*"; }
as_grain() { "$@"; }
grain_dir() { install -d -m"$1" "$2"; }
grain_write() { (umask 077; cat > "$2"; chmod "$1" "$2"); }
docker() {
  echo "docker $*" >> "$CALLS"
  case "$1" in
    image) [ -z "${STUB_NO_IMAGE:-}" ] ;;
    pull) return 0 ;;
    run) echo "postgres (PostgreSQL) ${STUB_PG_MAJOR:-16}.4 (Debian 16.4-1.pgdg120+2)" ;;
  esac
}
systemctl() {
  echo "systemctl $*" >> "$CALLS"
  case "$1" in is-active) [ -n "${STUB_ACTIVE:-}" ] ;; esac
}
`
	src := stubs + shellFunction(t, "installed_unit_flag") + block
	if err := os.WriteFile(lib, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// As the top of setup.sh leaves them.
	script := "set -euo pipefail\nGRAIN_DB_DRIVER=\"${GRAIN_DB_DRIVER:-}\"\nGRAIN_DB_DSN=\"${GRAIN_DB_DSN:-}\"\n" +
		"source \"$1\"\n" + steps
	cmd := exec.Command("bash", "-c", script, "bash", lib)
	var base []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GRAIN_") {
			base = append(base, kv)
		}
	}
	u, _ := exec.Command("id", "-un").Output()
	cmd.Env = append(base,
		"GRAIN_DATA_DIR="+h.data,
		"GRAIN_USER="+strings.TrimSpace(string(u)),
		"POSTGRES_UNIT="+h.postgresUnit,
		"POSTGRES_ENV_FILE="+h.envFile,
		"CALLS="+h.calls,
		"DOCKER_BIN=/usr/bin/docker",
	)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("running bash: %v", err)
		}
	}
	return string(out), err == nil
}

// resolve runs resolve_database and reports what it settled on.
func (h dbHost) resolve(t *testing.T, env ...string) (out string, ok bool) {
	t.Helper()
	return h.run(t, `resolve_database "`+h.unit+`" || exit 3
echo "DRIVER=$GRAIN_DB_DRIVER PROVISION=$GRAIN_POSTGRES_PROVISION MIGRATE=$GRAIN_POSTGRES_MIGRATE IMAGE=$GRAIN_POSTGRES_IMAGE PORT=$GRAIN_POSTGRES_PORT"
`, env...)
}

// install runs resolve_database and then ensure_local_postgres, as main
// would, and prints what the daemon's unit would gain.
func (h dbHost) install(t *testing.T, env ...string) (string, bool) {
	t.Helper()
	return h.run(t, `resolve_database "`+h.unit+`" || exit 3
ensure_local_postgres
postgres_daemon_unit
echo "AFTER=$POSTGRES_DAEMON_AFTER|REQUIRES=$POSTGRES_DAEMON_REQUIRES|ENV=$POSTGRES_DAEMON_ENV|ARGS=${POSTGRES_DOCKER_ARGS[*]:-}"
`, env...)
}

func (h dbHost) callLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// readAbs reads a file of the scratch host (read is the repository's).
func readAbs(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestAFreshInstallProvisionsALocalPostgres(t *testing.T) {
	h := newDBHost(t)
	out, ok := h.install(t)
	if !ok {
		t.Fatalf("fresh install failed:\n%s", out)
	}
	contains(t, out, "Database: PostgreSQL, on a local server this install provisions")
	contains(t, out, "AFTER= grain-postgres.service|REQUIRES= grain-postgres.service|ENV=EnvironmentFile="+h.envFile+"|ARGS=--env GRAIN_DB_DRIVER --env GRAIN_DB_DSN")

	unit := readAbs(t, h.postgresUnit)
	for _, want := range []string{
		"Environment=GRAIN_POSTGRES_IMAGE=mirror.gcr.io/library/postgres:16-bookworm@sha256:",
		"Environment=GRAIN_POSTGRES_PORT=5432",
		"Requires=docker.service",
		"--network host",
		"--volume " + h.data + "/postgres:/var/lib/postgresql/data",
		"--volume " + h.data + "/secrets/postgres-password:/run/grain/postgres-password:ro",
		"POSTGRES_PASSWORD_FILE=/run/grain/postgres-password",
		"--env POSTGRES_INITDB_ARGS=--auth=scram-sha-256",
		"postgres -c listen_addresses=127.0.0.1 -c port=5432",
		"pg_isready -q -h 127.0.0.1 -p 5432",
		"$$(seq 1 120)",
		"WantedBy=multi-user.target",
	} {
		contains(t, unit, want)
	}
	if !regexp.MustCompile(`--user [0-9]+:[0-9]+ `).MatchString(unit) {
		t.Errorf("the server does not run as the deployment's account:\n%s", unit)
	}
	absent(t, unit, "--publish")
	absent(t, unit, "0.0.0.0")

	password := readAbs(t, h.data, "secrets", "postgres-password")
	if !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(password) {
		t.Errorf("generated password %q is not 24 random bytes in hex", password)
	}
	if m := mode(t, filepath.Join(h.data, "secrets", "postgres-password")); m != 0o600 {
		t.Errorf("password file mode %o, want 600", m)
	}
	if m := mode(t, filepath.Join(h.data, "postgres")); m != 0o700 {
		t.Errorf("data directory mode %o, want 700", m)
	}
	env := readAbs(t, h.envFile)
	want := "GRAIN_DB_DRIVER=postgres\nGRAIN_DB_DSN=postgres://grain:" + password + "@127.0.0.1:5432/grain?sslmode=disable\n"
	if env != want {
		t.Errorf("%s reads %q, want %q", h.envFile, env, want)
	}
	if m := mode(t, h.envFile); m != 0o600 {
		t.Errorf("DSN file mode %o, want 600", m)
	}
	calls := h.callLog(t)
	contains(t, calls, "systemctl enable grain-postgres.service")
	contains(t, calls, "systemctl start grain-postgres.service")
	absent(t, calls, "upgrade-postgres")
}

func TestARerunKeepsTheProvisionedServerAndDoesNotRestartIt(t *testing.T) {
	h := newDBHost(t)
	if out, ok := h.install(t); !ok {
		t.Fatalf("first install: %s", out)
	}
	password := readAbs(t, h.data, "secrets", "postgres-password")
	os.Remove(h.calls)

	// A daemon unit is installed by now; the re-run still finds Postgres.
	if err := os.WriteFile(h.unit, []byte("ExecStart=/usr/bin/docker run grain daemon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, ok := h.install(t, "STUB_ACTIVE=1")
	if !ok {
		t.Fatalf("re-run failed:\n%s", out)
	}
	contains(t, out, "keeping the local PostgreSQL server")
	if got := readAbs(t, h.data, "secrets", "postgres-password"); got != password {
		t.Errorf("re-run replaced the password")
	}
	calls := h.callLog(t)
	absent(t, calls, "systemctl restart")
	contains(t, calls, "systemctl start grain-postgres.service")

	// A newer default image in a newer setup.sh is not an upgrade.
	unit := readAbs(t, h.postgresUnit)
	stale := regexp.MustCompile(`postgres:16-bookworm@sha256:[0-9a-f]{64}`).ReplaceAllString(unit, "postgres:16.1-bookworm")
	if err := os.WriteFile(h.postgresUnit, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ = h.resolve(t)
	contains(t, out, "IMAGE=mirror.gcr.io/library/postgres:16.1-bookworm")

	// Asking for one is, and restarts the server onto it.
	os.Remove(h.calls)
	out, ok = h.install(t, "STUB_ACTIVE=1", "GRAIN_POSTGRES_IMAGE=registry.example.com/postgres:16.5")
	if !ok {
		t.Fatalf("image change failed:\n%s", out)
	}
	contains(t, out, "changing from mirror.gcr.io/library/postgres:16.1-bookworm to registry.example.com/postgres:16.5")
	contains(t, readAbs(t, h.postgresUnit), "Environment=GRAIN_POSTGRES_IMAGE=registry.example.com/postgres:16.5")
	contains(t, h.callLog(t), "systemctl restart grain-postgres.service")
}

func TestARerunRewritesTrustInAnExistingClusterPgHBA(t *testing.T) {
	h := newDBHost(t)
	if out, ok := h.install(t); !ok {
		t.Fatalf("first install: %s", out)
	}
	hba := filepath.Join(h.data, "postgres", "pg_hba.conf")
	const trustHBA = `# CAUTION: local "trust" authentication
# local   all             all                                     trust
local   all             all                                     trust
host    all             all             127.0.0.1/32            trust
host    all             all             ::1/128                 trust
local   replication     all                                     trust
host    replication     all             127.0.0.1/32            trust
host    replication     all             ::1/128                 trust

host all all all scram-sha-256
`
	const wantHBA = `# CAUTION: local "trust" authentication
# local   all             all                                     trust
local   all             all                                     scram-sha-256
host    all             all             127.0.0.1/32            scram-sha-256
host    all             all             ::1/128                 scram-sha-256
local   replication     all                                     scram-sha-256
host    replication     all             127.0.0.1/32            scram-sha-256
host    replication     all             ::1/128                 scram-sha-256

host all all all scram-sha-256
`
	// Upgrade from an older unit that lacked POSTGRES_INITDB_ARGS: rewrites
	// pg_hba.conf and restarts grain-postgres.service because the unit changed.
	oldUnit := strings.ReplaceAll(readAbs(t, h.postgresUnit), " --env POSTGRES_INITDB_ARGS=--auth=scram-sha-256", "")
	if err := os.WriteFile(h.postgresUnit, []byte(oldUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hba, []byte(trustHBA), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(h.calls)

	out, ok := h.install(t, "STUB_ACTIVE=1")
	if !ok {
		t.Fatalf("upgrade run failed:\n%s", out)
	}
	contains(t, out, "replaced trust with scram-sha-256")
	if got := readAbs(t, hba); got != wantHBA {
		t.Errorf("pg_hba.conf after upgrade:\n%s\nwant:\n%s", got, wantHBA)
	}
	if m := mode(t, hba); m != 0o600 {
		t.Errorf("pg_hba.conf mode %o, want 600", m)
	}
	calls := h.callLog(t)
	contains(t, calls, "systemctl restart grain-postgres.service")
	absent(t, calls, "pg_ctl reload")

	// When the unit already matches (e.g. cold backup restored onto a host
	// with the current unit), rewriting pg_hba.conf reloads the running server
	// in place rather than restarting it.
	if err := os.WriteFile(hba, []byte(trustHBA), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(h.calls)
	out, ok = h.install(t, "STUB_ACTIVE=1")
	if !ok {
		t.Fatalf("reload run failed:\n%s", out)
	}
	if got := readAbs(t, hba); got != wantHBA {
		t.Errorf("pg_hba.conf after reload:\n%s\nwant:\n%s", got, wantHBA)
	}
	calls = h.callLog(t)
	absent(t, calls, "systemctl restart")
	contains(t, calls, "docker exec grain-postgres pg_ctl reload -D /var/lib/postgresql/data")

	// Once rewritten, later re-runs touch neither pg_hba.conf nor pg_ctl.
	os.Remove(h.calls)
	out, ok = h.install(t, "STUB_ACTIVE=1")
	if !ok {
		t.Fatalf("idempotent run failed:\n%s", out)
	}
	absent(t, out, "replaced trust")
	absent(t, h.callLog(t), "pg_ctl reload")

	// A symlink at pg_hba.conf is not followed (#2405).
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte(trustHBA), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(hba)
	if err := os.Symlink(victim, hba); err != nil {
		t.Fatal(err)
	}
	if out, ok := h.install(t, "STUB_ACTIVE=1"); !ok {
		t.Fatalf("symlink run failed:\n%s", out)
	}
	if got := readAbs(t, victim); got != trustHBA {
		t.Errorf("symlink target was modified:\n%s", got)
	}
}

func TestAnImageOfAnotherMajorVersionIsRefused(t *testing.T) {
	h := newDBHost(t)
	if out, ok := h.install(t); !ok {
		t.Fatalf("first install: %s", out)
	}
	if err := os.WriteFile(filepath.Join(h.data, "postgres", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, ok := h.install(t, "GRAIN_POSTGRES_IMAGE=mirror.gcr.io/library/postgres:17", "STUB_PG_MAJOR=17")
	if ok {
		t.Fatalf("a PostgreSQL 17 image was accepted for a 16 cluster:\n%s", out)
	}
	contains(t, out, "holds a PostgreSQL 16 cluster")
	contains(t, out, "Upgrading the")
}

func TestExplicitSQLiteProvisionsNothing(t *testing.T) {
	h := newDBHost(t)
	out, ok := h.install(t, "GRAIN_DB_DRIVER=sqlite")
	if !ok {
		t.Fatalf("--db-driver sqlite failed:\n%s", out)
	}
	contains(t, out, "Database: SQLite (--db-driver sqlite)")
	contains(t, out, "AFTER=|REQUIRES=|ENV=|ARGS=")
	if _, err := os.Stat(h.postgresUnit); err == nil {
		t.Errorf("--db-driver sqlite wrote %s", h.postgresUnit)
	}
	if calls := h.callLog(t); calls != "" {
		t.Errorf("--db-driver sqlite ran:\n%s", calls)
	}
}

func TestAnExternalDSNProvisionsNoServer(t *testing.T) {
	for _, dsn := range []string{
		"postgres://grain:secret@db.example.com:5432/grain",
		"host=/cloudsql/proj:region:inst dbname=grain user=grain",
	} {
		h := newDBHost(t)
		out, ok := h.install(t, "GRAIN_DB_DSN="+dsn)
		if !ok {
			t.Fatalf("external DSN %q failed:\n%s", dsn, out)
		}
		contains(t, out, "PostgreSQL at the GRAIN_DB_DSN given (no local server provisioned)")
		out, _ = h.resolve(t, "GRAIN_DB_DSN="+dsn)
		contains(t, out, "DRIVER=postgres PROVISION=0")
		if _, err := os.Stat(h.postgresUnit); err == nil {
			t.Errorf("an external DSN wrote %s", h.postgresUnit)
		}
		if calls := h.callLog(t); calls != "" {
			t.Errorf("an external DSN ran:\n%s", calls)
		}
	}
	// A DSN that is not a server, with no driver, is a SQLite path as it
	// always was.
	h := newDBHost(t)
	out, _ := h.resolve(t, "GRAIN_DB_DSN=/srv/grain/store")
	contains(t, out, "DRIVER= PROVISION=0")
}

func TestARerunOnASQLiteInstallStaysSQLite(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(h dbHost)
	}{
		{"a store", func(h dbHost) {
			os.MkdirAll(filepath.Join(h.data, "store"), 0o700)
			os.WriteFile(filepath.Join(h.data, "store", "grain.db"), nil, 0o600)
		}},
		{"an installed daemon unit", func(h dbHost) {
			os.WriteFile(h.unit, []byte("ExecStart=/usr/bin/docker run grain daemon -data-dir /x\n"), 0o644)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newDBHost(t)
			c.setup(h)
			out, ok := h.install(t)
			if !ok {
				t.Fatalf("re-run failed:\n%s", out)
			}
			contains(t, out, "keeping this host's SQLite store")
			contains(t, out, "grain upgrade-postgres")
			contains(t, out, "AFTER=|REQUIRES=|ENV=|ARGS=")
			if n := strings.Count(out, "upgrade-postgres"); n != 1 {
				t.Errorf("the hint is %d lines, want one", n)
			}
			if _, err := os.Stat(h.postgresUnit); err == nil {
				t.Errorf("a re-run on a SQLite install wrote %s", h.postgresUnit)
			}
			if calls := h.callLog(t); calls != "" {
				t.Errorf("a re-run on a SQLite install ran:\n%s", calls)
			}
			out, _ = h.resolve(t)
			contains(t, out, "DRIVER= PROVISION=0 MIGRATE=0")
		})
	}
}

func TestAnInstallOnAnotherPostgresIsLeftAlone(t *testing.T) {
	h := newDBHost(t)
	os.WriteFile(filepath.Join(h.data, "db-config.json"), []byte(`{"driver":"postgres"}`), 0o600)
	out, _ := h.resolve(t)
	contains(t, out, "keeping the PostgreSQL store this deployment records")
	contains(t, out, "PROVISION=0")
}

func TestExplicitPostgresOnASQLiteInstallMigrates(t *testing.T) {
	h := newDBHost(t)
	os.MkdirAll(filepath.Join(h.data, "store"), 0o700)
	os.WriteFile(filepath.Join(h.data, "store", "grain.db"), nil, 0o600)
	out, _ := h.resolve(t, "GRAIN_DB_DRIVER=postgres")
	contains(t, out, "migrating "+h.data+"/store into it")
	contains(t, out, "DRIVER=postgres PROVISION=1 MIGRATE=1")

	// A migration that never finished is taken up again, rather than
	// leaving the daemon on an empty database.
	os.WriteFile(filepath.Join(h.data, "secrets", "postgres-password"), []byte("abc123"), 0o600)
	out, _ = h.resolve(t)
	contains(t, out, "PROVISION=1 MIGRATE=1")
	os.WriteFile(filepath.Join(h.data, ".postgres-migrated"), nil, 0o600)
	out, _ = h.resolve(t)
	contains(t, out, "PROVISION=1 MIGRATE=0")
}

func TestSQLiteIsRefusedOnAPostgresInstall(t *testing.T) {
	h := newDBHost(t)
	if out, ok := h.install(t); !ok {
		t.Fatalf("first install: %s", out)
	}
	out, ok := h.resolve(t, "GRAIN_DB_DRIVER=sqlite")
	if ok {
		t.Fatalf("--db-driver sqlite was accepted on a local-PostgreSQL install:\n%s", out)
	}
	contains(t, out, "does not move a")
}

func TestBadPostgresSettingsAreRefused(t *testing.T) {
	for _, env := range []string{
		"GRAIN_DB_DRIVER=mysql",
		"GRAIN_POSTGRES_IMAGE=postgres:16 --privileged",
		"GRAIN_POSTGRES_IMAGE=postgres:16%n",
		"GRAIN_POSTGRES_PORT=54x",
		"GRAIN_POSTGRES_PORT=70000",
	} {
		h := newDBHost(t)
		if out, ok := h.resolve(t, env); ok {
			t.Errorf("%s was accepted:\n%s", env, out)
		}
	}
}

// The block's hooks are where they have to be in the rest of the script.
func TestTheDatabaseBlockIsWiredIntoTheInstall(t *testing.T) {
	text := setupText(t)
	code := setupCode(t)
	before(t, code, "if ! resolve_iap_audience", "if ! resolve_database \"$GRAIN_DAEMON_UNIT\"; then",
		"resolve_database runs with the other resolve_* steps, before anything is touched")
	m := body(t, code, "main() {")
	before(t, m, "setup_data_dir", "ensure_local_postgres", "the server's data directory is laid out first")
	before(t, m, "reformat_store_if_schema_changed", "ensure_local_postgres", "the schema step sees the store as it was")
	before(t, m, "ensure_local_postgres", "write_systemd_units", "the daemon's unit Requires= a server that is running")

	units := body(t, code, "write_systemd_units() {")
	contains(t, units, "Requires=docker.service${POSTGRES_DAEMON_REQUIRES}")
	contains(t, units, "${hostd_after}${POSTGRES_DAEMON_AFTER}")
	contains(t, units, "${POSTGRES_DAEMON_ENV}")
	contains(t, units, `[ "$GRAIN_POSTGRES_PROVISION" != 1 ]`)
	contains(t, body(t, code, "docker_run_args() {"), `DOCKER_ARGS+=("${POSTGRES_DOCKER_ARGS[@]}")`)
	contains(t, body(t, code, "install_cli_wrappers() {"), "--env-file ${POSTGRES_ENV_FILE}")

	uninstall := body(t, code, "uninstall_grain() {")
	contains(t, uninstall, "docker rm -f grain-postgres")
	contains(t, uninstall, "/etc/grain/postgres.env")
	contains(t, between(t, code, "UNINSTALL_UNITS=(", ")"), "grain-postgres.service")

	usage := body(t, text, "usage() {")
	absent(t, usage, "default: sqlite")
	contains(t, usage, "GRAIN_POSTGRES_IMAGE")
}
