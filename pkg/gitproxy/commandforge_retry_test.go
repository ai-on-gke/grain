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

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withLoginRetries sets CommandForgeTransport's waits before each new
// try of a request answered with a login page for one test.
func withLoginRetries(t *testing.T, waits ...time.Duration) {
	t.Helper()
	prev := commandForgeLoginRetries
	commandForgeLoginRetries = waits
	t.Cleanup(func() { commandForgeLoginRetries = prev })
}

// loginFound is what forgecli prints when the redirect it followed ended
// on the front end's login page (CommandForge.LoginPage finds it).
func loginFound() string {
	return gghResponse("200 OK", map[string]string{"Content-Type": "text/html"},
		`<html><form action="https://`+testLoginHost+`/?return=x">`)
}

func depotTransport(t *testing.T) *CommandForgeTransport {
	t.Helper()
	c := gghDepotSet(t, "")
	src, h, ok := c.forgeCommand("depot")
	if !ok {
		t.Fatal("depot is not a forgecli credential")
	}
	return newCommandForgeTransport(src, h, 10*time.Second)
}

func TestCommandForgeRetriesAReadAnsweredWithALoginPage(t *testing.T) {
	withLoginRetries(t, time.Millisecond, time.Millisecond, time.Millisecond)
	state := fakeGGHAPI(t)
	writeRaw(t, state, "api-response-1", loginFound())
	writeRaw(t, state, "api-response", gghResponse("200 OK", map[string]string{"Content-Type": "application/json"}, `{"ok":true}`))
	client := &http.Client{Transport: depotTransport(t)}

	resp, err := client.Get("https://depot.code.forge.test/api/v3/rate_limit")
	if err != nil {
		t.Fatalf("GET after one login page = %v; want it tried again", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != `{"ok":true}` {
		t.Fatalf("GET = %d %q", resp.StatusCode, body)
	}
	if runs := apiRuns(t, state); len(runs) != 2 {
		t.Fatalf("forgecli ran %d times; want 2 (one login page, one retry)", len(runs))
	}
}

func TestCommandForgeRetriesAFetchWithItsBody(t *testing.T) {
	withLoginRetries(t, time.Millisecond)
	state := fakeGGHAPI(t)
	writeRaw(t, state, "api-response-1", loginFound())
	writeRaw(t, state, "api-response", gghResponse("200 OK",
		map[string]string{"Content-Type": "application/x-git-upload-pack-result"}, "0008NAK\n"))
	client := &http.Client{Transport: depotTransport(t)}

	resp, err := client.Post("https://depot.code.forge.test/o/r.git/git-upload-pack",
		"application/x-git-upload-pack-request", strings.NewReader("0032want abc\n0000"))
	if err != nil {
		t.Fatalf("upload-pack after one login page = %v; want it tried again", err)
	}
	resp.Body.Close()
	if runs := apiRuns(t, state); len(runs) != 2 {
		t.Fatalf("forgecli ran %d times; want 2", len(runs))
	}
	for _, n := range []string{"1", "2"} {
		if stdin, _ := os.ReadFile(filepath.Join(state, "api-stdin-"+n)); string(stdin) != "0032want abc\n0000" {
			t.Fatalf("try %s sent body %q; want the whole request each time", n, stdin)
		}
	}
}

func TestCommandForgeNeverRetriesAPushOrAWrite(t *testing.T) {
	withLoginRetries(t, time.Millisecond, time.Millisecond)
	state := fakeGGHAPI(t)
	writeRaw(t, state, "api-response", loginFound())
	client := &http.Client{Transport: depotTransport(t)}

	resp, err := client.Post("https://depot.code.forge.test/o/r.git/git-receive-pack",
		"application/x-git-receive-pack-request", strings.NewReader("0000"))
	if err == nil {
		resp.Body.Close()
	}
	if _, ok := AsLoginRedirect(err); !ok {
		t.Fatalf("receive-pack = %v; want the LoginRedirectError", err)
	}
	if runs := apiRuns(t, state); len(runs) != 1 {
		t.Fatalf("receive-pack ran forgecli %d times; want once", len(runs))
	}

	resp, err = client.Post("https://depot.code.forge.test/api/v3/repos/o/r/pulls", "application/json", strings.NewReader(`{}`))
	if err == nil {
		resp.Body.Close()
	}
	if _, ok := AsLoginRedirect(err); !ok {
		t.Fatalf("REST POST = %v; want the LoginRedirectError", err)
	}
	if runs := apiRuns(t, state); len(runs) != 2 {
		t.Fatalf("forgecli ran %d times after a push and a POST; want 2", len(runs))
	}
}

func TestCommandForgeGivesUpAfterItsRetries(t *testing.T) {
	withLoginRetries(t, time.Millisecond, time.Millisecond)
	state := fakeGGHAPI(t)
	writeRaw(t, state, "api-response", loginFound())
	client := &http.Client{Transport: depotTransport(t)}

	resp, err := client.Get("https://depot.code.forge.test/api/v3/rate_limit")
	if err == nil {
		resp.Body.Close()
	}
	if _, ok := AsLoginRedirect(err); !ok {
		t.Fatalf("GET = %v; want the LoginRedirectError", err)
	}
	if runs := apiRuns(t, state); len(runs) != 3 {
		t.Fatalf("forgecli ran %d times; want 3 (the try and two retries)", len(runs))
	}
}

// A front end's own redirect to its login is a login a person renews:
// returned at once, never waited out.
func TestCommandForgeNeverRetriesAFrontEndsRedirectToItsLogin(t *testing.T) {
	withLoginRetries(t, time.Millisecond, time.Millisecond)
	state := fakeGGHAPI(t)
	writeRaw(t, state, "api-response", gghResponse("302 Found",
		map[string]string{"Location": "https://" + testLoginHost + "/?return=x"}, ""))
	client := &http.Client{Transport: depotTransport(t)}

	resp, err := client.Get("https://depot.code.forge.test/api/v3/rate_limit")
	if err == nil {
		resp.Body.Close()
	}
	if login, ok := AsLoginRedirect(err); !ok || !login.Redirected {
		t.Fatalf("GET = %v; want the LoginRedirectError, Redirected", err)
	}
	if runs := apiRuns(t, state); len(runs) != 1 {
		t.Fatalf("forgecli ran %d times; want once for a redirect to the login", len(runs))
	}
}

func TestCommandForgeRetryLoginDeclinesALoginOnlyAPersonRenews(t *testing.T) {
	withLoginRetries(t, time.Millisecond, time.Millisecond)
	state := fakeGGHAPI(t)
	writeRaw(t, state, "api-response", loginFound())
	tr := depotTransport(t)
	var asked []string
	tr.Forge.RetryLogin = func(location string) bool {
		asked = append(asked, location)
		return false
	}
	client := &http.Client{Transport: tr}

	resp, err := client.Get("https://depot.code.forge.test/api/v3/rate_limit")
	if err == nil {
		resp.Body.Close()
	}
	if _, ok := AsLoginRedirect(err); !ok {
		t.Fatalf("GET = %v; want the LoginRedirectError", err)
	}
	if runs := apiRuns(t, state); len(runs) != 1 {
		t.Fatalf("forgecli ran %d times; want once when RetryLogin says no", len(runs))
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "https://"+testLoginHost+"/") {
		t.Fatalf("RetryLogin asked about %q", asked)
	}
}
