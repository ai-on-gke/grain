package cli

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCLIEnv points the CLI at a cli.env of the test's own, holding
// content, and clears the two variables that would otherwise win over
// it.
func writeCLIEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli.env")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliEnvFileEnvVar, path)
	t.Setenv(serverEnvVar, "")
	t.Setenv(dataDirEnvVar, "")
	return path
}

// The variable, then the file setup.sh writes, then the built-in default
// -- for the server and the data directory alike.
func TestTheCLIFindsItsDaemonInCLIEnv(t *testing.T) {
	path := writeCLIEnv(t, "# Written by scripts/setup.sh\nGRAIN_SERVER=\"http://127.0.0.1:80\"\nexport GRAIN_DATA_DIR=/var/lib/grain/data\nOTHER=ignored\n")

	if url, source := serverSource(); url != "http://127.0.0.1:80" || source != path {
		t.Errorf("serverSource() = %q, %q; want the file's server, from the file", url, source)
	}
	if got := dataDirDefault(); got != "/var/lib/grain/data" {
		t.Errorf("dataDirDefault() = %q, want the file's", got)
	}

	t.Setenv(serverEnvVar, "http://127.0.0.1:9000")
	t.Setenv(dataDirEnvVar, "/srv/grain")
	if url, source := serverSource(); url != "http://127.0.0.1:9000" || source != "$GRAIN_SERVER" {
		t.Errorf("with GRAIN_SERVER set, serverSource() = %q, %q; want the variable", url, source)
	}
	if got := dataDirDefault(); got != "/srv/grain" {
		t.Errorf("with GRAIN_DATA_DIR set, dataDirDefault() = %q, want the variable", got)
	}

	t.Setenv(serverEnvVar, "")
	t.Setenv(dataDirEnvVar, "")
	t.Setenv(cliEnvFileEnvVar, filepath.Join(t.TempDir(), "absent"))
	if url, source := serverSource(); url != defaultServerURL || source != "the built-in default" {
		t.Errorf("with no file, serverSource() = %q, %q; want the built-in default", url, source)
	}
	if got := dataDirDefault(); got != "" {
		t.Errorf("with no file, dataDirDefault() = %q, want empty", got)
	}
}

// The file is data: a line that would run something if it were sourced
// sets nothing.
func TestCLIEnvIsReadAsData(t *testing.T) {
	writeCLIEnv(t, "touch /tmp/pwned\nGRAIN_SERVER\n=http://x\n")
	if url, _ := serverSource(); url != defaultServerURL {
		t.Errorf("serverSource() = %q, want the built-in default", url)
	}
}

// closedPort is a loopback port nothing is listening on.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// The 10-13 install: the CLI on the default port, the daemon on another.
// It used to retry the refused connection for two minutes in silence.
func TestARefusedConnectionFailsFastAndSaysWhereTheDaemonIs(t *testing.T) {
	writeCLIEnv(t, "GRAIN_SERVER=http://127.0.0.1:80\n")
	server := "http://" + closedPort(t)

	start := time.Now()
	err := runCLI([]string{"-server", server, "-connect-timeout", "500ms", "list"})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("a refused connection took %s to report", elapsed)
	}
	if err == nil {
		t.Fatal("runCLI succeeded against a closed port")
	}
	msg := err.Error()
	for _, want := range []string{"no grain daemon answered at " + server, "this host's daemon listens on http://127.0.0.1:80", "Set GRAIN_SERVER or pass -server"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not say %q", msg, want)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("error is more than one line: %q", msg)
	}
}

// A daemon that accepts the connection and never answers costs one
// -timeout for the whole command, not one per call it makes.
func TestAnUnansweredCallTimesOut(t *testing.T) {
	writeCLIEnv(t, "")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()

	start := time.Now()
	err = runCLI([]string{"-server", "http://" + l.Addr().String(), "-timeout", "300ms", "list"})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("an unanswered call took %s to report with -timeout 300ms", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "did not answer in time") || !strings.Contains(err.Error(), "-timeout 300ms") {
		t.Errorf("runCLI error = %v, want one that says the daemon did not answer within -timeout", err)
	}
}

// A timed-out call to a server other than the host's own says which one
// timed out before naming where the host's daemon listens -- the 10-15
// walkthrough's black-holed GRAIN_SERVER read "the grain daemon at
// <server>; this host's daemon listens on <cli.env> did not answer".
func TestATimeoutNamesTheHostDaemonAfterTheServerThatTimedOut(t *testing.T) {
	writeCLIEnv(t, "GRAIN_SERVER=http://127.0.0.1:8420\n")
	err := explainDaemonError(os.ErrDeadlineExceeded, cliTarget{
		server: "http://10.255.255.1:4242", source: "$GRAIN_SERVER", connect: 5 * time.Second, call: 2 * time.Minute,
	})
	msg := err.Error()
	want := "the grain daemon at http://10.255.255.1:4242 (from $GRAIN_SERVER) did not answer in time " +
		"(-connect-timeout 5s, -timeout 2m0s); this host's daemon listens on http://127.0.0.1:8420 per "
	if !strings.HasPrefix(msg, want) {
		t.Errorf("error = %q\nwant it to start %q", msg, want)
	}
}

// A 401 says why the CLI had no credential: here, a local API token it
// is not allowed to read.
func TestAnUnauthorisedCallNamesTheUnreadableToken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"sign in to use this server's API"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	token := filepath.Join(dir, "local-api-token")
	if err := os.WriteFile(token, []byte("grain-local-x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(token, 0); err != nil {
		t.Fatal(err)
	}
	writeCLIEnv(t, "GRAIN_DATA_DIR="+dir+"\n")
	t.Setenv("GRAIN_LOCAL_API_TOKEN_FILE", "")

	err := runCLI([]string{"-server", srv.URL, "list"})
	if err == nil {
		t.Fatal("runCLI succeeded against a 401")
	}
	msg := err.Error()
	for _, want := range []string{"refused this CLI's credentials", "sign in to use this server's API", token + " exists", "cannot read it", "sudo -u"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not say %q", msg, want)
		}
	}
	if strings.Contains(msg, "; hint:") {
		t.Errorf("error %q repeats the client's own hint beside this one", msg)
	}
	// The token is mode 0600 and setup.sh adds nobody to grain's group,
	// so the hint must not send the installing user there (the
	// 2026-10-26 single-tenant install walkthrough).
	for _, wrong := range []string{"usermod", "join the group", "does that for whoever ran it"} {
		if strings.Contains(msg, wrong) {
			t.Errorf("error %q suggests %q, which cannot work on a 0600 token", msg, wrong)
		}
	}
}

// An answer from the daemon that is not about reaching it is left as
// the daemon said it.
func TestADaemonErrorIsNotRewritten(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"task 9 not found"}`))
	}))
	defer srv.Close()
	writeCLIEnv(t, "")
	err := runCLI([]string{"-server", srv.URL, "get", "9"})
	if err == nil || strings.Contains(err.Error(), "grain daemon") {
		t.Errorf("runCLI error = %v, want the daemon's own 404", err)
	}
}
