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

	"github.com/ai-on-gke/grain/pkg/ui"
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
// from, and the URL itself: $GRAIN_SERVER, then the server of the
// context $GRAIN_CONTEXT or config.json selects (context.go), then
// cliEnvFile, then defaultServerURL (external-cli.spec.md#XC-3).
func serverSource() (url, source string) {
	return serverSourceFor(envContext())
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
	return cliSocketHTTPClient("", connect, call)
}

// cliSocketHTTPClient is cliHTTPClient dialing the unix socket at socket
// for every request, whatever host its URL names; "" dials the URL's.
func cliSocketHTTPClient(socket string, connect, call time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}
	t.DialContext = dialer.DialContext
	if socket != "" {
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		}
	}
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

// tokenSource names the explicit bearer credential HTTPClient will send
// before falling back to the local API token or a Google ID token:
// "-token", "$GRAIN_TOKEN" (or one of its aliases), or "".
func tokenSource(flagToken string) string {
	if flagToken != "" {
		return "-token"
	}
	for _, env := range ui.TokenEnvVars {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return "$" + env
		}
	}
	return ""
}

// hostSocket is the daemon's API socket the CLI dials in place of server
// (ui.HostAPISocket): only for a server on this host, and only when
// nothing else -- -token, the environment's token, a context's own
// credential -- says how to sign in, since on the socket the caller is
// who its account maps to.
func hostSocket(server, credential string) string {
	if credential != "" || !ui.IsLoopbackURL(server) {
		return ""
	}
	return ui.HostAPISocket()
}

// cliTarget is what explainDaemonError needs to know about where the CLI
// went: the URL, where it came from, what supplied its token, and the
// flags that bound the call.
type cliTarget struct {
	server, source string
	tokenSource    string
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
	var classified *cliError
	if errors.As(err, &classified) {
		return err // a command's own verdict, already said
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
	if strings.HasPrefix(t.source, "context ") {
		fix = "Check the server of " + t.source + " (`grain context show`), or pass -server"
	}

	switch {
	case daemonRefused(err):
		return &cliError{exit: exitUnreachable, code: "daemon_unreachable",
			err: fmt.Errorf("no grain daemon answered at %s%s. %s (%v)", where, hint, fix, rootCause(err))}
	case daemonTimedOut(err):
		// The hint follows the sentence it qualifies: put between the
		// address and "did not answer", it read as this host's daemon
		// being the one that timed out.
		return &cliError{exit: exitUnreachable, code: "daemon_unreachable",
			err: fmt.Errorf("the grain daemon at %s did not answer in time (-connect-timeout %s, -timeout %s)%s. "+
				"%s, or pass a longer -timeout for a slow call (%v)", where, t.connect, t.call, hint, fix, rootCause(err))}
	}
	if unauthorized(err) {
		return &cliError{exit: exitAuth, code: "unauthenticated",
			err: fmt.Errorf("the grain daemon at %s refused this CLI's credentials (%s): %s",
				where, serverMessage(err), credentialHint(t.server, t.tokenSource))}
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

// credentialHint says why the CLI's call was refused on credentials, and
// what to do about it: an explicit -token or environment token that was
// rejected is named as such; a non-loopback server never receives the
// local API token; and on loopback with no explicit token, the local API
// token file is inspected.
func credentialHint(server, tokenSource string) string {
	switch {
	case tokenSource == "-token":
		return "the token passed with -token was refused"
	case strings.HasPrefix(tokenSource, "$"):
		return "the token from " + tokenSource + " was refused"
	case strings.HasPrefix(tokenSource, "context "):
		return "the credential of " + tokenSource + " was refused; run grain login --refresh"
	case strings.HasPrefix(tokenSource, "the API socket "):
		return "this account was refused on " + tokenSource + ": the daemon's -host-accounts (or -deployment-owner) maps it to nobody " +
			"the allowlist admits; set " + ui.APISocketEnv + "=none to sign in over -server instead"
	case !ui.IsLoopbackURL(server):
		return "no -token was given; pass a token with -token or set GRAIN_TOKEN"
	}
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
