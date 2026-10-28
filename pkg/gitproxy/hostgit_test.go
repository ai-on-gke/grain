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

//go:build linux

package gitproxy

// An ambient credential in a daemon that runs in a container beside grain
// hostd: every gh/forgecli run happens in the host-git capability server
// that a real hostd starts from its allowlist -- this test binary, run as
// that server (hostGitServerAsked) -- and not in the daemon's own process.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// hostGitServerEnv is set, by the test allowlist's set_env, on the
// process hostd starts: it makes this test binary serve host-git.
const hostGitServerEnv = "GITPROXY_TEST_HOSTGIT_SERVER"

func hostGitServerAsked() bool { return os.Getenv(hostGitServerEnv) == "1" }

// hostGitServerMain is `grain capability host-git`, as this test binary.
func hostGitServerMain() int {
	s, err := protocol.NewCapabilityServer(HostGitEntry)
	if err == nil {
		err = ServeHostGit(s)
	}
	if err == nil {
		err = s.ServeStdio(context.Background(), os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// startHostGitHostd serves a real grain hostd whose allowlist is body
// with %s standing for this test binary, and installs a HostGitClient on
// it for the test. The daemon side looks containerised from here on.
func startHostGitHostd(t *testing.T, body string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	// Not t.TempDir: a unix socket's path has to fit in 108 bytes.
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	if err := os.WriteFile(allow, []byte(strings.ReplaceAll(body, "%s", exe)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket:          filepath.Join(dir, "hostd.sock"),
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Logf:            func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; ; i++ {
		if _, err := os.Stat(srv.Socket); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("hostd never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(UseHostGit(&HostGitClient{Hostd: hostd.Client{Socket: srv.Socket}, Stderr: os.Stderr}))
	t.Setenv("GRAIN_IN_CONTAINER", "1")
}

// hostGitAllowlist offers this test binary as the host-git entry.
const hostGitAllowlist = "capabilities:\n  host-git:\n    command: %s\n    set_env: {" + hostGitServerEnv + ": \"1\"}\n"

func TestAmbientTokenComesFromTheHostThroughHostd(t *testing.T) {
	state := fakeHostTools(t)
	startHostGitHostd(t, hostGitAllowlist)
	c, _ := ambientSet(t)
	if err := AmbientAvailable(); err != nil {
		t.Fatalf("a containerised daemon beside a hostd offering host-git: %v", err)
	}
	if err := c.SetAmbient("op", "", "gh"); err != nil {
		t.Fatal(err)
	}
	writeState(t, state, "gh-token", "gho_from_the_host")
	cred, ok := c.Get("op")
	if !ok || cred.Token == nil || *cred.Token != "gho_from_the_host" {
		t.Fatalf("Get = %+v, %v; want the host's gh token", cred, ok)
	}
	if got := calls(t, state, "gh-calls"); got != 1 {
		t.Fatalf("gh ran %d times on the host, want 1", got)
	}

	// git credential fill is not something hostd runs for the daemon.
	writeState(t, state, "git-password", "from-a-helper")
	if _, _, err := AmbientToken("github.com", AmbientSourceGit); err == nil || !strings.Contains(err.Error(), "grain hostd") {
		t.Fatalf("the git source through hostd: err = %v", err)
	}
	if got := calls(t, state, "git-calls"); got != 0 {
		t.Fatalf("git ran %d times", got)
	}

	// A login that fails says so as it would on the host.
	os.Remove(filepath.Join(state, "gh-token"))
	if _, _, err := AmbientToken("github.com", "gh"); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("no gh login on the host: err = %v", err)
	}
}

func TestForgeCallsGoThroughTheHostsToolThroughHostd(t *testing.T) {
	state := fakeGGHAPI(t)
	startHostGitHostd(t, hostGitAllowlist)
	c := gghDepotSet(t, "")
	writeRaw(t, state, "api-response", gghResponse("201 Created",
		map[string]string{"Content-Type": "application/json", "X-Github-Request-Id": "abc"}, `{"number":7}`))

	transport, err := c.ForgeTransport("depot")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.Request("POST", "/repos/o/r/pulls", map[string]string{
		"Accept":        "application/vnd.github+json",
		"Content-Type":  "application/json",
		"Authorization": "token forgecli-token-depot.code.forge.test",
	}, []byte(`{"title":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 201 || string(resp.Body) != `{"number":7}` {
		t.Fatalf("response = %d %q", resp.Status, resp.Body)
	}
	runs := apiRuns(t, state)
	if len(runs) != 1 {
		t.Fatalf("api runs = %q", runs)
	}
	args := strings.Join(runs[0], " ")
	for _, want := range []string{"api --include --method POST", "-H Accept: application/vnd.github+json", "--input - repos/o/r/pulls"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if stdin, _ := os.ReadFile(filepath.Join(state, "api-stdin-1")); string(stdin) != `{"title":"t"}` {
		t.Fatalf("body = %q", stdin)
	}

	// A lapsed login comes back as the login redirect, not a response.
	writeRaw(t, state, "api-response", gghResponse("302 Found", map[string]string{"Location": "https://" + testLoginHost + "/x"}, ""))
	if _, err := transport.Request("GET", "/user", nil, nil); err == nil || !strings.Contains(err.Error(), "forgecli-login") {
		t.Fatalf("login redirect through hostd: err = %v", err)
	}
}

func TestHostGitServerRunsOnlyRegisteredSourcesForTheirHosts(t *testing.T) {
	fakeGGHAPI(t)
	startHostGitHostd(t, hostGitAllowlist)
	c := activeHostGit()
	if _, err := c.token("sh", "github.com"); err == nil || !strings.Contains(err.Error(), "not an ambient source") {
		t.Fatalf("an unregistered source: err = %v", err)
	}
	if _, err := c.token(testForgeSource, "github.com"); err == nil || !strings.Contains(err.Error(), "only for") {
		t.Fatalf("forgecli asked about a host it does not claim: err = %v", err)
	}
	tr := newCommandForgeTransport(mustSource(t, testForgeSource), CredentialHost{Host: "depot.code.forge.test"}, time.Second)
	req, _ := http.NewRequest("GET", "https://elsewhere.example/o/r.git/info/refs", nil)
	if _, err := c.forge(tr, req); err == nil || !strings.Contains(err.Error(), "not a URL on depot.code.forge.test") {
		t.Fatalf("a request for another host: err = %v", err)
	}
}

func TestAmbientIsRefusedWhenHostdOffersNoHostGit(t *testing.T) {
	fakeHostTools(t)
	startHostGitHostd(t, "capabilities: {}\n")
	err := AmbientAvailable()
	if err == nil || !strings.Contains(err.Error(), `offers no capability entry "host-git"`) {
		t.Fatalf("AmbientAvailable = %v", err)
	}
	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "", "gh"); err == nil {
		t.Fatal("saved an ambient credential that hostd cannot serve")
	}
}

func mustSource(t *testing.T, name string) AmbientCommandSource {
	t.Helper()
	src, ok := ambientCommandSource(name)
	if !ok {
		t.Fatalf("no source %q", name)
	}
	return src
}
