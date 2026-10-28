package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwsalmon/grain/pkg/ui"
)

// How the task CLI finds and reaches its daemon (grain/task-3688).
//
// The 10-13 host-mode install is what this is for: /usr/local/bin/grain
// was the daemon's own binary, run by the operator in the shell that had
// just run setup.sh, so /etc/profile.d/grain.sh -- read only by a new
// login shell -- had set nothing. The CLI fell back to
// http://127.0.0.1:8420, the daemon was on 127.0.0.1:80, and the refused
// connection was retried silently for two minutes under the HTTPClient's
// restart-gap policy (ui.DefaultRetryPolicy, there so an agent's
// mcpserver call survives an upgrade's restart), which to the operator
// was a hang.
//
// So: setup.sh writes cliEnvFile, which says where this host's daemon
// listens and where its data directory is, and the CLI reads it whatever
// shell it is in -- an environment variable first, then the file, then
// the built-in default. Every call has a connect timeout and a response
// timeout, a refused connection is retried for no longer than the
// connect timeout, and a call that fails because nothing answered, or
// because the daemon turned the CLI's credentials away, ends in one line
// that says what to do about it.

// defaultCLIEnvFile is the file scripts/setup.sh writes, in both install
// modes (write_cli_profile). cliEnvFileEnvVar names another one -- a
// test's, or a host laid out by hand -- and an empty value of it turns
// the file off.
const (
	defaultCLIEnvFile = "/etc/grain/cli.env"
	cliEnvFileEnvVar  = "GRAIN_CLI_ENV"
)

// The defaults for -connect-timeout and -timeout. The response timeout
// is longer than the daemon's longest long-poll (a minute) and bounds
// only the wait for the daemon to start answering: a body that is
// streaming -- an artifact download -- takes as long as it takes.
const (
	defaultConnectTimeout = 5 * time.Second
	defaultCallTimeout    = 2 * time.Minute
)

// cliEnv is what cliEnvFile says. It is read as data, KEY=VALUE per
// line, never sourced, and only the two keys below mean anything.
type cliEnv struct {
	path    string
	server  string
	dataDir string
}

func cliEnvPath() string {
	if v, ok := os.LookupEnv(cliEnvFileEnvVar); ok {
		return strings.TrimSpace(v)
	}
	return defaultCLIEnvFile
}

// readCLIEnv reads cliEnvPath. A missing or unreadable file is an empty
// cliEnv: it is a convenience a host may not have, never a requirement.
func readCLIEnv() cliEnv {
	env := cliEnv{path: cliEnvPath()}
	if env.path == "" {
		return env
	}
	f, err := os.Open(env.path)
	if err != nil {
		return env
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		switch strings.TrimSpace(key) {
		case serverEnvVar:
			env.server = value
		case dataDirEnvVar:
			env.dataDir = value
		}
	}
	return env
}

// serverSource is where the server the CLI would use by default comes
// from, and the URL itself: $GRAIN_SERVER, then cliEnvFile, then
// defaultServerURL.
func serverSource() (url, source string) {
	if v := strings.TrimSpace(os.Getenv(serverEnvVar)); v != "" {
		return v, "$" + serverEnvVar
	}
	if env := readCLIEnv(); env.server != "" {
		return env.server, env.path
	}
	return defaultServerURL, "the built-in default"
}

// useCLIEnvDataDir sets GRAIN_DATA_DIR from cliEnvFile when the
// environment does not say it. The local API token is found through
// GRAIN_DATA_DIR (pkg/ui's localAPIToken), which a shell that never read
// the profile script does not have -- the one that just ran
// google/scripts/cloudtop-install.sh, say -- and against a single-tenant
// daemon, which trusts no loopback caller, a CLI that presents no token
// is refused. Every mode that talks to the daemon through ui.HTTPClient
// calls this before it makes one: runCLI and `grain sync` (task 4496).
func useCLIEnvDataDir() {
	if os.Getenv(dataDirEnvVar) != "" {
		return
	}
	if dir := readCLIEnv().dataDir; dir != "" {
		os.Setenv(dataDirEnvVar, dir)
	}
}

// cliHTTPClient is the http.Client every task-CLI call is sent with.
func cliHTTPClient(connect, call time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = connect
	t.ResponseHeaderTimeout = call
	return &http.Client{Transport: &stickyTimeout{next: t}}
}

// stickyTimeout fails every request after the first one that timed out
// with that same error. One command makes several calls -- `grain list`
// asks for the list's configuration before the list -- and without this
// a daemon that accepts connections and never answers costs one -timeout
// per call rather than one per command.
type stickyTimeout struct {
	next http.RoundTripper
	mu   sync.Mutex
	err  error
}

func (s *stickyTimeout) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	resp, err := s.next.RoundTrip(req)
	if err != nil && daemonTimedOut(err) && req.Context().Err() == nil {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}
	return resp, err
}

// cliRetryPolicy waits out a daemon that refuses connections -- a
// restart -- for no longer than the connect timeout, so an operator
// whose CLI is pointed at the wrong port hears so in seconds rather than
// after ui.DefaultRetryPolicy's two minutes.
func cliRetryPolicy(connect time.Duration) *ui.RetryPolicy {
	return &ui.RetryPolicy{Budget: connect, Initial: 200 * time.Millisecond, Max: time.Second}
}

// cliTarget is what explainDaemonError needs to know about where the CLI
// went: the URL, where it came from, and the flags that bound the call.
type cliTarget struct {
	server, source string
	connect, call  time.Duration
}

// explainDaemonError turns a failure to reach the daemon, or the daemon
// refusing the CLI's credentials, into one line that says what to do.
// Anything else -- the daemon's own answer to a request -- is returned
// as it is.
func explainDaemonError(err error, t cliTarget) error {
	if err == nil {
		return nil
	}
	where := t.server
	switch t.source {
	case "-server":
	case "the built-in default":
		where += " (the default)"
	default:
		where += " (from " + t.source + ")"
	}
	hint := hostDaemonHint(t.server)
	fix := "Set " + serverEnvVar + " or pass -server"

	switch {
	case daemonRefused(err):
		return fmt.Errorf("no grain daemon answered at %s%s. %s (%v)", where, hint, fix, rootCause(err))
	case daemonTimedOut(err):
		// The hint follows the sentence it qualifies: put between the
		// address and "did not answer", it read as this host's daemon
		// being the one that timed out.
		return fmt.Errorf("the grain daemon at %s did not answer in time (-connect-timeout %s, -timeout %s)%s. "+
			"%s, or pass a longer -timeout for a slow call (%v)", where, t.connect, t.call, hint, fix, rootCause(err))
	}
	if unauthorized(err) {
		return fmt.Errorf("the grain daemon at %s refused this CLI's credentials (%s): %s", where, serverMessage(err), credentialHint())
	}
	return err
}

// hostDaemonHint names where this host's own daemon listens when that is
// somewhere other than server -- the case that cost the 10-13 install.
func hostDaemonHint(server string) string {
	env := readCLIEnv()
	if env.server == "" || strings.TrimRight(env.server, "/") == strings.TrimRight(server, "/") {
		return ""
	}
	return fmt.Sprintf("; this host's daemon listens on %s per %s", env.server, env.path)
}

// credentialHint says why a loopback CLI had no credential the daemon
// accepts, and how to get one: the local API token is the credential,
// and the usual reason is that it is there but not readable by whoever
// is running this.
func credentialHint() string {
	path := ui.LocalAPITokenFile()
	if path == "" {
		return "no local API token was found (set " + dataDirEnvVar + " to the daemon's data directory), " +
			"and no -token was given; pass the access code with -token (`grain access-code` prints it)"
	}
	_, err := os.ReadFile(path)
	switch {
	case err == nil:
		return "the local API token at " + path + " was sent and refused -- it is stale if the daemon has " +
			"been reinstalled; restart grain-daemon.service to re-read it"
	case errors.Is(err, os.ErrPermission):
		// The file is mode 0600 (pkg/ui/localtoken.go), so joining its
		// group would not help, and setup.sh adds nobody to it.
		return fmt.Sprintf("the local API token at %s exists, but %s cannot read it: only the daemon's "+
			"account and root can. Run the CLI as that account (sudo -u %s grain ...)",
			path, currentUser(), ui.FileOwner(path))
	case errors.Is(err, os.ErrNotExist):
		return "there is no local API token at " + path + " -- " + dataDirEnvVar + " may name the wrong data " +
			"directory, or the daemon has not started there yet"
	default:
		return fmt.Sprintf("the local API token at %s could not be read: %v", path, err)
	}
}

func daemonRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused") {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && !dnsErr.IsTimeout
}

func daemonTimedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(err.Error(), "timeout awaiting response headers")
}

// unauthorized reports a 401 or 403 that is about who the CLI is rather
// than about what it asked for: the daemon's answer to a request with no
// credential it accepts. A 403 for an admin-only action from a signed-in
// account is that account's answer, and is left alone.
func unauthorized(err error) bool {
	msg := err.Error()
	if strings.Contains(msg, "(401 ") {
		return true
	}
	var forbidden *ui.ForbiddenError
	return errors.As(err, &forbidden) && strings.Contains(msg, "does not have access to this server")
}

// serverMessage is what the daemon said, without the hint HTTPClient
// appends to a loopback 401: explainDaemonError says that itself.
func serverMessage(err error) string {
	msg, _, _ := strings.Cut(err.Error(), "; hint:")
	return msg
}

// rootCause is the innermost error, which is the one with something to
// say (connection refused, i/o timeout) once the rest has been said in a
// sentence.
func rootCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

func currentUser() string {
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	return fmt.Sprintf("uid %d", os.Getuid())
}
