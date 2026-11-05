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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
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
	for _, env := range ui.TokenEnvVars {
		t.Setenv(env, "")
	}

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

// A 401 names the credential that was actually offered -- -token, an
// environment variable, or the loopback local API token -- and never gives
// local-token advice for a non-loopback server.
func TestAnUnauthorisedCallNamesTheCredentialSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"nope"}`))
	}))
	defer srv.Close()

	clearTokenEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("GRAIN_LOCAL_API_TOKEN_FILE", "")
		for _, env := range ui.TokenEnvVars {
			t.Setenv(env, "")
		}
	}

	t.Run("flag token", func(t *testing.T) {
		writeCLIEnv(t, "")
		clearTokenEnv(t)
		t.Setenv("GRAIN_TOKEN", "from-env")

		err := runCLI([]string{"-server", srv.URL, "-token", "wrong", "list"})
		if err == nil {
			t.Fatal("runCLI succeeded against a 401")
		}
		msg := err.Error()
		want := ": the token passed with -token was refused"
		if !strings.HasSuffix(msg, want) {
			t.Errorf("error = %q, want suffix %q", msg, want)
		}
		for _, wrong := range []string{"no -token was given", "local API token"} {
			if strings.Contains(msg, wrong) {
				t.Errorf("error %q should not say %q", msg, wrong)
			}
		}
	})

	for _, env := range ui.TokenEnvVars {
		t.Run("env "+env, func(t *testing.T) {
			writeCLIEnv(t, "")
			clearTokenEnv(t)
			t.Setenv(env, "wrong")

			err := runCLI([]string{"-server", srv.URL, "list"})
			if err == nil {
				t.Fatal("runCLI succeeded against a 401")
			}
			msg := err.Error()
			want := ": the token from $" + env + " was refused"
			if !strings.HasSuffix(msg, want) {
				t.Errorf("error = %q, want suffix %q", msg, want)
			}
			for _, wrong := range []string{"no -token was given", "local API token"} {
				if strings.Contains(msg, wrong) {
					t.Errorf("error %q should not say %q", msg, wrong)
				}
			}
		})
	}

	t.Run("loopback local token absent or stale", func(t *testing.T) {
		writeCLIEnv(t, "")
		clearTokenEnv(t)
		err := runCLI([]string{"-server", srv.URL, "list"})
		if err == nil || !strings.Contains(err.Error(), "no local API token was found") {
			t.Errorf("absent data dir error = %v, want missing local API token hint", err)
		}

		dir := t.TempDir()
		writeCLIEnv(t, "GRAIN_DATA_DIR="+dir+"\n")
		clearTokenEnv(t)
		err = runCLI([]string{"-server", srv.URL, "list"})
		if err == nil || !strings.Contains(err.Error(), "there is no local API token at "+filepath.Join(dir, "local-api-token")) {
			t.Errorf("missing token file error = %v, want missing file hint", err)
		}

		tokenPath := filepath.Join(dir, "local-api-token")
		if err := os.WriteFile(tokenPath, []byte("grain-local-stale\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeCLIEnv(t, "GRAIN_DATA_DIR="+dir+"\n")
		clearTokenEnv(t)
		err = runCLI([]string{"-server", srv.URL, "list"})
		if err == nil || !strings.Contains(err.Error(), "the local API token at "+tokenPath+" was sent and refused") {
			t.Errorf("stale token file error = %v, want stale token hint", err)
		}
	})

	t.Run("non-loopback server", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "local-api-token"), []byte("grain-local-x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeCLIEnv(t, "GRAIN_DATA_DIR="+dir+"\n")
		clearTokenEnv(t)
		t.Setenv(dataDirEnvVar, dir)

		unauth := errors.New("grain server: nope (401 Unauthorized)")
		err := explainDaemonError(unauth, cliTarget{
			server: "https://grain.example.com",
			source: "-server",
		})
		msg := err.Error()
		want := ": no -token was given; pass a token with -token or set GRAIN_TOKEN"
		if !strings.HasSuffix(msg, want) {
			t.Errorf("error = %q, want suffix %q", msg, want)
		}
		for _, localAdvice := range []string{"local API token", "local-api-token", "GRAIN_DATA_DIR", "grain access-code", "sudo -u", "grain-daemon.service"} {
			if strings.Contains(msg, localAdvice) {
				t.Errorf("non-loopback error %q contains local-token advice %q", msg, localAdvice)
			}
		}

		err = explainDaemonError(unauth, cliTarget{
			server:      "https://grain.example.com",
			source:      "-server",
			tokenSource: "-token",
		})
		if !strings.HasSuffix(err.Error(), ": the token passed with -token was refused") {
			t.Errorf("non-loopback -token error = %q", err)
		}

		err = explainDaemonError(unauth, cliTarget{
			server:      "https://grain.example.com",
			source:      "-server",
			tokenSource: "$GRAIN_TOKEN",
		})
		if !strings.HasSuffix(err.Error(), ": the token from $GRAIN_TOKEN was refused") {
			t.Errorf("non-loopback $GRAIN_TOKEN error = %q", err)
		}
	})
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

// TestContextCommandsAndServerPrecedence is external-cli.spec.md#XC-2 and
// #XC-3: -context, then $GRAIN_CONTEXT, then config.json's current picks
// the context; -server, then $GRAIN_SERVER, then that context's server,
// then cli.env, then the default picks the daemon; an unknown context is
// exit 2; the context's credential goes to its own server only; and
// `grain context` lists, uses, shows and deletes contexts.
func TestContextCommandsAndServerPrecedence(t *testing.T) {
	dir := useConfigHome(t)
	envFile := writeCLIEnv(t, "GRAIN_SERVER=http://127.0.0.1:81\n")
	var hits [2]atomic.Value
	servers := make([]*httptest.Server, 2)
	for i := range servers {
		i := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[i].Store(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"acceptedAuth":["external-token"],"externalAgentApi":true,"authenticated":true,"principal":"agent:external:x"}`)
		}))
		defer servers[i].Close()
	}
	store := credentialStore{}
	locA, err := store.store("a", "gma_secret-of-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveContexts(&contextConfig{Current: "a", Contexts: map[string]*cliContext{
		"a": {Server: servers[0].URL, Auth: "external-token", Credential: locA},
		"b": {Server: servers[1].URL, Auth: "local"},
	}}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, flag, envContext, envServer string
		wantURL, wantSource               string
	}{
		{"current", "", "", "", servers[0].URL, "context a"},
		{"$GRAIN_CONTEXT", "", "b", "", servers[1].URL, "context b"},
		{"-context over $GRAIN_CONTEXT", "a", "b", "", servers[0].URL, "context a"},
		{"$GRAIN_SERVER over the context", "a", "", "http://127.0.0.1:9", "http://127.0.0.1:9", "$GRAIN_SERVER"},
	} {
		t.Setenv(contextEnvVar, tc.envContext)
		t.Setenv(serverEnvVar, tc.envServer)
		sel, err := selectContext(tc.flag)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if url, source := serverSourceFor(sel); url != tc.wantURL || source != tc.wantSource {
			t.Errorf("%s: server = %q from %q, want %q from %q", tc.name, url, source, tc.wantURL, tc.wantSource)
		}
	}
	t.Setenv(contextEnvVar, "")
	t.Setenv(serverEnvVar, "")

	// The context's credential goes to its server; -server elsewhere
	// gets none of it.
	if err := runCLI([]string{"whoami", "--context", "a"}); err != nil {
		t.Fatal(err)
	}
	if got := hits[0].Load(); got != "Bearer gma_secret-of-a" {
		t.Errorf("context a's server got Authorization %v, want its credential", got)
	}
	if err := runCLI([]string{"-server", servers[1].URL, "whoami"}); err != nil {
		t.Fatal(err)
	}
	if got := hits[1].Load(); got == "Bearer gma_secret-of-a" {
		t.Error("-server pointed elsewhere was sent context a's credential")
	}

	for _, args := range [][]string{{"-context", "nope", "whoami"}, {"context", "use", "nope"}, {"context", "show", "nope"}, {"context", "delete", "nope"}} {
		if err := runCLI(args); exitOf(err) != exitUsage || !strings.Contains(fmt.Sprint(err), `"nope"`) {
			t.Errorf("grain %s = %v, want exit 2 naming the context", strings.Join(args, " "), err)
		}
	}
	t.Setenv(contextEnvVar, "nope")
	if err := runCLI([]string{"whoami"}); exitOf(err) != exitUsage {
		t.Errorf("$GRAIN_CONTEXT=nope: %v, want exit 2", err)
	}
	t.Setenv(contextEnvVar, "")

	// A context's server that is not answering is named as the context's.
	if err := saveContexts(&contextConfig{Current: "a", Contexts: map[string]*cliContext{
		"a":    {Server: servers[0].URL, Auth: "external-token", Credential: locA},
		"b":    {Server: servers[1].URL, Auth: "local"},
		"down": {Server: "http://127.0.0.1:1", Auth: "none"},
	}}); err != nil {
		t.Fatal(err)
	}
	err = runCLI([]string{"-connect-timeout", "300ms", "--context", "down", "whoami"})
	if exitOf(err) != exitUnreachable || !strings.Contains(err.Error(), "(from context down)") {
		t.Errorf("unreachable context server: %v, want exit 4 naming context down", err)
	}

	out := captureStdout(t, func() {
		if err := runCLI([]string{"context", "list", "--json"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, `"name": "a"`) || !strings.Contains(out, `"current": true`) || strings.Contains(out, "gma_") {
		t.Errorf("context list --json = %s", out)
	}
	if err := runCLI([]string{"context", "use", "b"}); err != nil {
		t.Fatal(err)
	}
	if sel, _ := selectContext(""); sel.name != "b" {
		t.Errorf("after use b, the current context is %q", sel.name)
	}
	out = captureStdout(t, func() {
		if err := runCLI([]string{"context", "show", "a", "-json"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, servers[0].URL) || !strings.Contains(out, "file:a") {
		t.Errorf("context show a = %s", out)
	}
	if err := runCLI([]string{"context", "delete", "a"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadContexts()
	if _, ok := cfg.Contexts["a"]; ok || cfg.Current != "b" {
		t.Errorf("after delete a: %+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials", "a")); !os.IsNotExist(err) {
		t.Errorf("delete a left its credential file (%v)", err)
	}
	if err := runCLI([]string{"context", "delete", "b"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadContexts()
	if cfg.Current != "" {
		t.Errorf("deleting the current context left current = %q", cfg.Current)
	}

	// With no context selected, cli.env and then the default.
	cfg.Contexts = map[string]*cliContext{}
	if err := saveContexts(cfg); err != nil {
		t.Fatal(err)
	}
	if url, source := serverSource(); url != "http://127.0.0.1:81" || source != envFile {
		t.Errorf("with no context, serverSource() = %q, %q; want cli.env's", url, source)
	}
}
