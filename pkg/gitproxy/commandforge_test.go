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

package gitproxy

// Forge traffic for a forgecli credential: every call one `forgecli api` run, with
// a fake forgecli on PATH standing in for the real one, which only a Cloudtop
// has (docs/git-hosts.md section 12 describes the seam).

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeGGHAPI is fakeGGH with `forgecli api` too. Each api run appends its
// argv (one argument a line, then a "--" line) to api-args, its GH_HOST
// and HTTPS_PROXY to api-env, and its stdin to api-stdin-<n>; it prints
// api-response-<n> when that file exists and api-response otherwise, and
// exits with api-exit (0 when absent). api-sleep, when present, is how
// long it sleeps first.
func fakeGGHAPI(t *testing.T) string {
	t.Helper()
	state := fakeHostTools(t)
	bin := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	body := `#!/bin/sh
S="` + state + `"
echo "GH_HOST=$GH_HOST $1 $2" >> "$S/forgecli-calls"
if [ "$1 $2" = "auth token" ]; then
  echo "forgecli-token-$GH_HOST"
  exit 0
fi
[ "$1" = "api" ] || { echo "unexpected forgecli $*" >&2; exit 2; }
n=$(grep -c . "$S/api-n" 2>/dev/null || echo 0)
n=$((n+1))
echo x >> "$S/api-n"
for a in "$@"; do printf '%s\n' "$a" >> "$S/api-args"; done
echo -- >> "$S/api-args"
echo "GH_HOST=$GH_HOST HTTPS_PROXY=$HTTPS_PROXY" >> "$S/api-env"
[ -f "$S/api-sleep" ] && sleep "$(cat "$S/api-sleep")"
for a in "$@"; do
  [ "$a" = "--input" ] && cat > "$S/api-stdin-$n"
done
if [ -f "$S/api-response-$n" ]; then cat "$S/api-response-$n"; elif [ -f "$S/api-response" ]; then cat "$S/api-response"; fi
[ -f "$S/api-stderr" ] && cat "$S/api-stderr" >&2
code=0
[ -f "$S/api-exit" ] && code=$(cat "$S/api-exit")
exit $code
`
	if err := os.WriteFile(filepath.Join(bin, "forgecli"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return state
}

// gghResponse is what `forgecli api --include` prints for one answer.
func gghResponse(status string, headers map[string]string, body string) string {
	var b strings.Builder
	b.WriteString("HTTP/2.0 " + status + "\r\n")
	for k, v := range headers {
		b.WriteString(k + ": " + v + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.String()
}

func writeRaw(t *testing.T, state, file, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, file), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

// apiRuns is each recorded api run's argv.
func apiRuns(t *testing.T, state string) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(state, "api-args"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var runs [][]string
	var cur []string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "--" {
			runs = append(runs, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return runs
}

func gghDepotSet(t *testing.T, proxy string) *CredentialSet {
	t.Helper()
	c, _ := ambientSet(t)
	if err := c.SetAmbientOn("depot", CredentialHost{Host: depotHost, Kind: model.GitHostGitHub, Proxy: proxy}, AmbientSourceAuto); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGGHForgeCallsGoThroughGGHAPI(t *testing.T) {
	state := fakeGGHAPI(t)
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
	if v, _ := headerValue(resp.Headers, "X-GitHub-Request-Id"); v != "abc" {
		t.Fatalf("headers = %v", resp.Headers)
	}
	runs := apiRuns(t, state)
	if len(runs) != 1 {
		t.Fatalf("api runs = %q", runs)
	}
	args := strings.Join(runs[0], " ")
	for _, want := range []string{"api --include --method POST", "-H Accept: application/vnd.github+json",
		"-H Content-Type: application/json", "--input - repos/o/r/pulls"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "forgecli-token") || strings.Contains(args, "Authorization") {
		t.Fatalf("the token reached forgecli's command line: %q", args)
	}
	if !strings.HasSuffix(args, " repos/o/r/pulls") {
		t.Fatalf("endpoint not relative to the API: %q", args)
	}
	if stdin, _ := os.ReadFile(filepath.Join(state, "api-stdin-1")); string(stdin) != `{"title":"t"}` {
		t.Fatalf("body = %q", stdin)
	}
	if env, _ := os.ReadFile(filepath.Join(state, "api-env")); !strings.Contains(string(env), "GH_HOST=depot.code.forge.test HTTPS_PROXY=\n") {
		t.Fatalf("env = %q", env)
	}
}

// GraphQL on an Enterprise Server is beside /api/v3, at /api/graphql,
// which is where RealTransport sends it; through the tool it is the
// tool's own "graphql" endpoint, the one gh posts wherever its host
// serves GraphQL -- never /api/v3/graphql, which the host refuses.
func TestGGHGraphQLGoesToTheToolsGraphQLEndpoint(t *testing.T) {
	state := fakeGGHAPI(t)
	c := gghDepotSet(t, "")
	writeRaw(t, state, "api-response", gghResponse("200 OK",
		map[string]string{"Content-Type": "application/json"},
		`{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`))

	transport, err := c.ForgeTransport("depot")
	if err != nil {
		t.Fatal(err)
	}
	client := github.NewClient(transport, nil)
	if err := client.ResolveReviewThread("experimental", "grain", "PRRT_1"); err != nil {
		t.Fatal(err)
	}
	runs := apiRuns(t, state)
	if len(runs) != 1 {
		t.Fatalf("api runs = %q", runs)
	}
	if args := strings.Join(runs[0], " "); !strings.HasSuffix(args, " --input - graphql") {
		t.Fatalf("args %q do not end in the tool's graphql endpoint", args)
	}
}

func TestGGHPassesNon2xxThroughAndHonoursAProxy(t *testing.T) {
	state := fakeGGHAPI(t)
	c := gghDepotSet(t, "http://127.0.0.1:3128")
	writeRaw(t, state, "api-response", gghResponse("404 Not Found",
		map[string]string{"Content-Type": "application/json"}, `{"message":"Not Found"}`))
	writeRaw(t, state, "api-stderr", "gh: Not Found (HTTP 404)\n")
	writeRaw(t, state, "api-exit", "1")
	transport, err := c.ForgeTransport("depot")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.Request("GET", "/repos/o/missing", nil, nil)
	if err != nil || resp.Status != 404 || !strings.Contains(string(resp.Body), "Not Found") {
		t.Fatalf("404 = %d %q, %v; want it passed through", resp.Status, resp.Body, err)
	}
	if runs := apiRuns(t, state); strings.Contains(strings.Join(runs[0], " "), "--input") {
		t.Fatalf("a GET sent a body: %q", runs[0])
	}
	if env, _ := os.ReadFile(filepath.Join(state, "api-env")); !strings.Contains(string(env), "HTTPS_PROXY=http://127.0.0.1:3128") {
		t.Fatalf("the recorded proxy did not reach forgecli: %q", env)
	}
}

func TestGGHPaginatesThroughReach(t *testing.T) {
	state := fakeGGHAPI(t)
	c := gghDepotSet(t, "")
	var page1 []string
	for i := 0; i < 100; i++ {
		page1 = append(page1, `{"full_name":"corp/r`+strconv.Itoa(i)+`","permissions":{"push":false}}`)
	}
	writeRaw(t, state, "api-response-1", gghResponse("200 OK", map[string]string{"Content-Type": "application/json"},
		"["+strings.Join(page1, ",")+"]"))
	writeRaw(t, state, "api-response-2", gghResponse("200 OK", map[string]string{"Content-Type": "application/json"},
		`[{"full_name":"experimental/bsalmon-test","permissions":{"push":true}}]`))

	// The deployment's own API is github.com's, and never asked.
	dotcom := github.NewFakeTransport()
	reach := &Reach{Credentials: c, Host: "github.com", API: dotcom}
	repos, ok, err := reach.Repos("depot")
	if err != nil || !ok || len(repos) != 101 {
		t.Fatalf("Repos = %d repos, %v, %v", len(repos), ok, err)
	}
	if last := repos[100]; last.Repo != depotHost+"/experimental/bsalmon-test" || !last.Push {
		t.Fatalf("last = %+v", last)
	}
	if len(dotcom.Calls) != 0 {
		t.Fatalf("github.com was asked about depot: %+v", dotcom.Calls)
	}
	runs := apiRuns(t, state)
	if len(runs) != 2 || !strings.HasSuffix(strings.Join(runs[1], " "), " user/repos?per_page=100&page=2") {
		t.Fatalf("runs = %q", runs)
	}
}

func TestForgeCLILoginRedirectSaysRunTheLoginCommand(t *testing.T) {
	state := fakeGGHAPI(t)
	c := gghDepotSet(t, "")
	reach := &Reach{Credentials: c, Host: "github.com", API: github.NewFakeTransport()}

	// The 302 itself, as the forge's front end sends it.
	writeRaw(t, state, "api-response", gghResponse("302 Found",
		map[string]string{"Location": "https://login.forge.test/?return=x&authLevel=2000000"}, ""))
	_, _, err := reach.Repos("depot")
	if _, ok := AsLoginRedirect(err); !ok || !strings.Contains(err.Error(), "forge login needed: run `forgecli-login` as ") {
		t.Fatalf("302 to login = %v; want the login message", err)
	}

	// The login page itself, the redirect followed.
	writeRaw(t, state, "api-response", gghResponse("200 OK", map[string]string{"Content-Type": "text/html; charset=utf-8"},
		`<html><form action="https://login.forge.test/">`))
	if _, err := c.AmbientIdentityOf("depot"); err == nil || !strings.Contains(err.Error(), "run `forgecli-login`") {
		t.Fatalf("login page = %v; want the login message", err)
	}

	// And grain's own client, for anything still sent direct.
	h := CredentialHost{Host: depotHost, Kind: model.GitHostGitHub}
	if err := loginRedirectCheck(&http.Request{URL: mustURL(t, "https://login.forge.test/x")},
		[]*http.Request{{URL: mustURL(t, "https://depot.code.forge.test/api/v3/user")}}); err == nil {
		t.Fatalf("a redirect to the corp login was followed")
	}
	if h.HTTPClient(time.Second).CheckRedirect == nil {
		t.Fatalf("HTTPClient follows a corp login redirect")
	}
}

func TestGGHTimesOut(t *testing.T) {
	state := fakeGGHAPI(t)
	c := gghDepotSet(t, "")
	writeRaw(t, state, "api-sleep", "5")
	src, h, ok := c.forgeCommand("depot")
	if !ok {
		t.Fatal("depot is not a forgecli credential")
	}
	client := commandForgeClient(src, h, 300*time.Millisecond)
	start := time.Now()
	resp, err := client.Get("https://depot.code.forge.test/api/v3/user")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("a forgecli that never answers answered")
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("timing out took %s", took)
	}
}

func TestGGHGitForwardGoesThroughGGHWithTheWholeURL(t *testing.T) {
	state := fakeGGHAPI(t)
	c := gghDepotSet(t, "")
	writeRaw(t, state, "api-response", gghResponse("200 OK",
		map[string]string{"Content-Type": "application/x-git-upload-pack-result"}, "0008NAK\n"))
	cred, ok := c.Get("depot")
	if !ok || cred.Token == nil {
		t.Fatalf("Get(depot) = %+v", cred)
	}
	p := &GitProxy{Credentials: c, TeamCredentials: ladderConnections{creds: c}, Forwarder: &RealForwarder{Host: "github.com", UseTLS: true}}
	fw, err := p.forwarderFor(cred)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := fw.Forward("POST", "/o/r.git/git-upload-pack", "", map[string]string{
		"Content-Type": "application/x-git-upload-pack-request",
	}, []byte("0000"), cred.Token)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Stream)
	resp.Stream.Close()
	if resp.Status != 200 || string(body) != "0008NAK\n" {
		t.Fatalf("forward = %d %q", resp.Status, body)
	}
	runs := apiRuns(t, state)
	args := strings.Join(runs[len(runs)-1], " ")
	if !strings.HasSuffix(args, " https://depot.code.forge.test/o/r.git/git-upload-pack") ||
		!strings.Contains(args, "-H Content-Type: application/x-git-upload-pack-request") {
		t.Fatalf("git forward args = %q", args)
	}
	// depot's git endpoints take the token only as Basic x-access-token
	// (forgecli's own "token" header gets 401): the one header with a
	// credential in it forgecli is given, and only for git.
	if want := "-H Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+*cred.Token)); !strings.Contains(args, want) ||
		strings.Count(args, "Authorization") != 1 {
		t.Fatalf("git forward args = %q; want exactly %q", args, want)
	}
	if stdin, _ := os.ReadFile(filepath.Join(state, "api-stdin-1")); string(stdin) != "0000" {
		t.Fatalf("git body = %q", stdin)
	}

	// With a proxy recorded, the operator's route is used as before.
	c2 := gghDepotSet(t, "http://127.0.0.1:1")
	cred2, _ := c2.Get("depot")
	fw, _ = (&GitProxy{Credentials: c2, TeamCredentials: ladderConnections{creds: c2}, Forwarder: p.Forwarder}).forwarderFor(cred2)
	if rf := fw.(*RealForwarder); rf.Client == nil {
		t.Fatalf("no client for a proxied host")
	} else if _, isGGH := rf.Client.Transport.(*CommandForgeTransport); isGGH {
		t.Fatalf("a recorded proxy was ignored for git")
	}
}

func TestGGHHostNeedsNoProxy(t *testing.T) {
	fakeGGHAPI(t)
	c := gghDepotSet(t, "")
	if !c.ForgeViaCommand("depot") || !AmbientForgeViaCommand(depotHost, AmbientSourceAuto) {
		t.Fatalf("depot's forgecli credential does not go through forgecli")
	}
	if AmbientForgeViaCommand("github.com", AmbientSourceAuto) {
		t.Fatalf("github.com goes through forgecli")
	}
	if h := c.HostOf("depot"); h.Proxy != "" || h.CheckTransport() != nil {
		t.Fatalf("a forgecli host without a proxy is refused: %+v %v", h, h.CheckTransport())
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
