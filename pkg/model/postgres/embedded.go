package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var binaryDirs = []string{
	"/usr/lib/postgresql/15/bin",
	"/usr/lib/postgresql/16/bin",
	"/usr/lib/postgresql/14/bin",
	"/usr/lib/postgresql/17/bin",
	"/usr/local/bin",
	"/usr/bin",
}

// findBinary locates a postgres binary (e.g. initdb, pg_ctl, postgres, pg_isready).
func findBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range binaryDirs {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && (fi.Mode()&0111 != 0) {
			return p, nil
		}
	}
	matches, _ := filepath.Glob("/usr/lib/postgresql/*/bin/" + name)
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", fmt.Errorf("postgres binary %q not found", name)
}

// pgCommand builds an invocation of a postgres binary with pgEnv, so a
// new call in this file cannot hand the server the daemon's environment
// -- its API keys, tokens and cloud credentials -- by being written the
// obvious way. The server started from here lives as long as the
// cluster does, and none of that is anything it needs.
func pgCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = pgEnv()
	return cmd
}

// pgEnv is the environment pgCommand passes through: enough to find
// binaries, name the user (initdb and postgres look it up), place
// temporary files and keep the server's time zone and locale what they
// were. Nothing else in the daemon's environment is postgres's business.
func pgEnv() []string {
	var env []string
	for _, name := range []string{
		"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TZ", "LANG", "LC_ALL",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// IsAvailable reports whether PostgreSQL binaries (initdb and pg_ctl or postgres) are available.
func IsAvailable() bool {
	if _, err := findBinary("initdb"); err != nil {
		return false
	}
	if _, err := findBinary("postgres"); err != nil {
		if _, err := findBinary("pg_ctl"); err != nil {
			return false
		}
	}
	return true
}

func findAvailablePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:5432")
	if err == nil {
		_ = l.Close()
		return 5432
	}
	l, err = net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	return 5432
}

func readPostmasterPort(pgDir string) int {
	data, err := os.ReadFile(filepath.Join(pgDir, "postmaster.pid"))
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) >= 4 {
		if p, err := strconv.Atoi(strings.TrimSpace(lines[3])); err == nil && p > 0 {
			return p
		}
	}
	return 0
}

// ensurePasswdEntry ensures that the current process UID and GID have entries in /etc/passwd and /etc/group.
// PostgreSQL binaries (such as initdb, postgres, and pg_ctl) require getpwuid(geteuid()) to succeed;
// in containers or restricted environments running as an arbitrary UID (e.g. UID 1000) without
// an /etc/passwd entry, initdb fails with "could not look up effective user ID <uid>: user does not exist".
func ensurePasswdEntry() {
	uid := os.Geteuid()
	gid := os.Getegid()

	if content, err := os.ReadFile("/etc/passwd"); err == nil {
		found := false
		for _, line := range strings.Split(string(content), "\n") {
			parts := strings.Split(line, ":")
			if len(parts) >= 3 && parts[2] == strconv.Itoa(uid) {
				found = true
				break
			}
		}
		if !found {
			if f, err := os.OpenFile("/etc/passwd", os.O_APPEND|os.O_WRONLY, 0644); err == nil {
				username := os.Getenv("USER")
				if username == "" {
					username = os.Getenv("LOGNAME")
				}
				if username == "" {
					username = "grain"
				}
				home := os.Getenv("HOME")
				if home == "" {
					home = "/tmp"
				}
				_, _ = fmt.Fprintf(f, "%s:x:%d:%d::%s:/bin/bash\n", username, uid, gid, home)
				_ = f.Close()
			}
		}
	}

	if content, err := os.ReadFile("/etc/group"); err == nil {
		found := false
		for _, line := range strings.Split(string(content), "\n") {
			parts := strings.Split(line, ":")
			if len(parts) >= 3 && parts[2] == strconv.Itoa(gid) {
				found = true
				break
			}
		}
		if !found {
			if f, err := os.OpenFile("/etc/group", os.O_APPEND|os.O_WRONLY, 0644); err == nil {
				_, _ = fmt.Fprintf(f, "grain:x:%d:\n", gid)
				_ = f.Close()
			}
		}
	}
}

// Option adjusts how EnsureRunning starts a server.
type Option func(*options)

type options struct {
	withoutCrashSafety bool
}

// WithoutCrashSafety starts the server with fsync and full_page_writes
// off. It is for test clusters only -- a throwaway data directory whose
// loss after a crash costs nothing -- and it is passed to the postgres
// process on its command line rather than written into
// postgresql.conf, so it applies to the server this call starts and
// never to the cluster on disk: whoever starts that data directory next
// without the option gets a crash-safe server. It has no effect on a
// server that was already running.
func WithoutCrashSafety() Option {
	return func(o *options) { o.withoutCrashSafety = true }
}

// embeddedTuning is appended to the postgresql.conf of every cluster
// EnsureRunning creates.
//
// synchronous_commit=off is a durability trade made deliberately, and it
// is the same one pkg/model/sqlite makes with synchronous=NORMAL -- read
// the doc comment on sqlite.Open for why that trade suits grain's data.
// A commit returns before its WAL record is flushed; the WAL writer
// flushes it within a few hundred milliseconds (3x wal_writer_delay).
// A crash of the postgres process loses nothing, and an OS crash or a
// power cut loses at most that last fraction of a second of commits,
// but the cluster always comes back consistent: PostgreSQL documents
// this setting as unable to cause corruption.
//
// fsync and full_page_writes stay on, and that is the line not to
// cross. With either off PostgreSQL documents that an OS crash or power
// loss can leave the cluster unrecoverably corrupt -- torn pages, or
// data files that never reached the disk behind a WAL that says they
// did -- which is losing the store rather than losing its tail. Tests
// that want the speed ask for it with WithoutCrashSafety, which never
// touches this file.
const embeddedTuning = "\n# Grain embedded tuning\nsynchronous_commit = off\nmax_connections = 200\nshared_buffers = 128MB\n"

// legacyEmbeddedTuning is what clusters created before embeddedTuning
// lost its fsync=off and full_page_writes=off carry in their
// postgresql.conf. repairDurability rewrites it.
const legacyEmbeddedTuning = "\n# Grain embedded tuning\nfsync = off\nsynchronous_commit = off\nfull_page_writes = off\nmax_connections = 200\nshared_buffers = 128MB\n"

// repairDurability rewrites the unsafe tuning block an older grain wrote
// into confPath, if it is there, and reports whether it changed the
// file. Only the exact block grain wrote is touched: a setting an
// operator wrote themselves is theirs to keep.
func repairDurability(confPath string) (bool, error) {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return false, err
	}
	conf := string(data)
	if !strings.Contains(conf, legacyEmbeddedTuning) {
		return false, nil
	}
	conf = strings.Replace(conf, legacyEmbeddedTuning, embeddedTuning, 1)
	if err := os.WriteFile(confPath, []byte(conf), 0600); err != nil {
		return false, err
	}
	return true, nil
}

// maxStartAttempts bounds how many ports EnsureRunning tries. A port
// findAvailablePort hands back is only free at the moment it looked, and
// something else can bind it before postgres does, so a start that fails
// is retried on a fresh port rather than failing the call.
const maxStartAttempts = 3

// EnsureRunning initializes and starts an included PostgreSQL database inside
// dataDir/postgres if it is not already running. It returns the connection Config
// and a stop function to shut down the server when done.
//
// The stop function only stops a server this call started. When the
// server was already running -- typically the daemon's own, found by a
// later caller in the same data directory -- it is a no-op, so a caller
// that only wanted to look at the database cannot take it away from
// everyone else using it.
func EnsureRunning(ctx context.Context, dataDir string, opts ...Option) (Config, func() error, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	ensurePasswdEntry()

	pgDir := filepath.Join(dataDir, "postgres")
	if err := os.MkdirAll(pgDir, 0700); err != nil {
		return Config{}, nil, fmt.Errorf("creating postgres data directory: %w", err)
	}

	initdbPath, err := findBinary("initdb")
	if err != nil {
		return Config{}, nil, fmt.Errorf("locating initdb: %w", err)
	}

	confPath := filepath.Join(pgDir, "postgresql.conf")
	repaired := false
	// Initialize database cluster if not already initialized
	if _, err := os.Stat(filepath.Join(pgDir, "PG_VERSION")); os.IsNotExist(err) {
		cmd := pgCommand(ctx, initdbPath, "-D", pgDir, "-A", "trust", "-U", "postgres", "--no-locale", "-E", "UTF8")
		if out, err := cmd.CombinedOutput(); err != nil {
			return Config{}, nil, fmt.Errorf("initializing postgres cluster in %s: %w (%s)", pgDir, err, string(out))
		}
		f, err := os.OpenFile(confPath, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return Config{}, nil, fmt.Errorf("opening %s: %w", confPath, err)
		}
		_, err = f.WriteString(embeddedTuning)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return Config{}, nil, fmt.Errorf("writing %s: %w", confPath, err)
		}
	} else if repaired, err = repairDurability(confPath); err != nil {
		return Config{}, nil, fmt.Errorf("re-enabling fsync and full_page_writes in %s: %w", confPath, err)
	}

	pgCtlPath, _ := findBinary("pg_ctl")
	isRunning := false
	if pgCtlPath != "" {
		statusCmd := pgCommand(ctx, pgCtlPath, "-D", pgDir, "status")
		if err := statusCmd.Run(); err == nil {
			isRunning = true
		}
	} else if _, err := os.Stat(filepath.Join(pgDir, "postmaster.pid")); err == nil {
		isRunning = true
	}

	noop := func() error { return nil }

	if isRunning {
		if repaired && pgCtlPath != "" {
			// fsync and full_page_writes are both reloadable, so a
			// running server picks the repaired file up without a
			// restart. A failure is not fatal: the file is fixed, and the
			// next start reads it.
			_ = pgCommand(ctx, pgCtlPath, "-D", pgDir, "reload").Run()
		}
		port := readPostmasterPort(pgDir)
		if port == 0 {
			port = findAvailablePort()
		}
		return embeddedConfig(pgDir, port), noop, nil
	}

	var serverArgs []string
	if o.withoutCrashSafety {
		serverArgs = append(serverArgs, "-F", "-c", "full_page_writes=off")
	}

	var lastErr error
	for attempt := 0; attempt < maxStartAttempts; attempt++ {
		port := findAvailablePort()
		stop, err := startServer(ctx, pgDir, pgCtlPath, port, serverArgs)
		if err == nil {
			return embeddedConfig(pgDir, port), stop, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return Config{}, nil, lastErr
}

func embeddedConfig(pgDir string, port int) Config {
	return Config{
		Host:     pgDir,
		Port:     port,
		User:     "postgres",
		Database: "postgres",
		SSLMode:  "disable",
	}
}

// startServer starts postgres on port and waits for it to accept
// connections, returning the function that stops it. A server that
// starts but never becomes ready is stopped again before the error is
// returned, so a retry on another port does not find it in the way.
func startServer(ctx context.Context, pgDir, pgCtlPath string, port int, serverArgs []string) (func() error, error) {
	var postgresCmd *exec.Cmd
	if pgCtlPath != "" {
		serverOpts := append([]string{"-k", pgDir, "-h", "127.0.0.1", "-p", strconv.Itoa(port)}, serverArgs...)
		// pg_ctl start detaches the server from itself, so tying pg_ctl
		// to ctx bounds the wait without the server's lifetime being
		// bound to ctx too.
		startCmd := pgCommand(ctx, pgCtlPath, "-D", pgDir, "-l", filepath.Join(pgDir, "postgres.log"),
			"-o", strings.Join(serverOpts, " "), "start")
		if out, err := startCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("starting postgres server: %w (%s)", err, string(out))
		}
	} else {
		postgresPath, err := findBinary("postgres")
		if err != nil {
			return nil, fmt.Errorf("locating postgres binary: %w", err)
		}
		args := append([]string{"-D", pgDir, "-k", pgDir, "-h", "127.0.0.1", "-p", strconv.Itoa(port)}, serverArgs...)
		// Not ctx: the server has to outlive it, and it is often a
		// single request's.
		cmd := pgCommand(context.Background(), postgresPath, args...)
		cmd.Dir = pgDir
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("starting postgres process: %w", err)
		}
		postgresCmd = cmd
	}

	stop := func() error {
		if pgCtlPath != "" {
			cmd := pgCommand(context.Background(), pgCtlPath, "-D", pgDir, "-m", "fast", "stop")
			return cmd.Run()
		}
		if postgresCmd != nil && postgresCmd.Process != nil {
			_ = postgresCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- postgresCmd.Wait() }()
			select {
			case err := <-done:
				return err
			case <-time.After(3 * time.Second):
				return postgresCmd.Process.Kill()
			}
		}
		return nil
	}

	if err := waitReady(ctx, pgDir, port); err != nil {
		_ = stop()
		return nil, err
	}
	return stop, nil
}

// waitReady waits up to five seconds, or until ctx is done, for the
// server on port to accept connections.
func waitReady(ctx context.Context, pgDir string, port int) error {
	pgIsReadyPath, _ := findBinary("pg_isready")
	for i := 0; i < 50; i++ {
		if pgIsReadyPath != "" {
			checkCmd := pgCommand(ctx, pgIsReadyPath, "-h", pgDir, "-p", strconv.Itoa(port))
			if err := checkCmd.Run(); err == nil {
				return nil
			}
		} else {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for postgres to become ready: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("timed out waiting for postgres to become ready")
}
