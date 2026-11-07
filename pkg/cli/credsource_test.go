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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestRegisterCredentialSourceAndHTTPClientRefusesLoginRedirectAndHTML is
// the CLI's half of external-cli.spec.md#XC-10 (pkg/ui's
// httpclient_auth_test.go is HTTPClient's): a context whose auth names a
// registered source sends that source's token; a source whose sign-in
// has lapsed ends the command at once with exit 3 and its fix, in -json
// too; and a proxy's redirect to its login page, or its HTML sign-in
// page, is exit 3 "not signed in", never a JSON parse error.
func TestRegisterCredentialSourceAndHTTPClientRefusesLoginRedirectAndHTML(t *testing.T) {
	useConfigHome(t)
	t.Setenv(ui.LocalAPITokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	for _, env := range ui.TokenEnvVars {
		t.Setenv(env, "")
	}
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>sign in</html>")
	}))
	defer login.Close()
	loginHost := strings.TrimPrefix(login.URL, "http://")
	t.Cleanup(authfix.RegisterRedirect("test-proxy", func(u *url.URL) (authfix.Fix, bool) {
		return authfix.Fix{Summary: "proxy session expired", Action: "run `proxy-login`"}, u.Host == loginHost
	}))

	var gotAuth atomic.Value
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/redirect/api/auth/whoami":
			http.Redirect(w, r, login.URL+"/login?continue="+url.QueryEscape(r.URL.String()), http.StatusTemporaryRedirect)
		case "/html/api/auth/whoami":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<!doctype html><title>Sign in</title>")
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"acceptedAuth":["proxy"],"authenticated":true,"principal":"human:jane@example.com"}`)
		}
	}))
	defer api.Close()

	var lapsed atomic.Bool
	RegisterCredentialSource("corp-proxy", CredentialSourceFunc(func(_ context.Context, req CredentialRequest) (string, error) {
		if lapsed.Load() {
			return "", SignInRequired("the team proxy", "proxy-login")
		}
		return "proxy-token-for-" + req.Context, nil
	}))
	t.Cleanup(func() { RegisterCredentialSource("corp-proxy", nil) })
	if err := saveContexts(&contextConfig{Current: "corp", Contexts: map[string]*cliContext{
		"corp":     {Server: api.URL, Auth: "corp-proxy"},
		"redirect": {Server: api.URL + "/redirect", Auth: "none"},
		"html":     {Server: api.URL + "/html", Auth: "none"},
		"mystery":  {Server: api.URL, Auth: "no-such-source"},
	}}); err != nil {
		t.Fatal(err)
	}

	if err := runCLI([]string{"whoami"}); err != nil {
		t.Fatal(err)
	}
	if got := gotAuth.Load(); got != "Bearer proxy-token-for-corp" {
		t.Errorf("Authorization = %v, want the registered source's token", got)
	}

	lapsed.Store(true)
	var stderr strings.Builder
	code := runTaskCLI([]string{"whoami", "--json"}, &stderr)
	var report jsonError
	if err := json.Unmarshal([]byte(stderr.String()), &report); err != nil {
		t.Fatalf("-json stderr = %q: %v", stderr.String(), err)
	}
	if code != exitAuth || report.Error.Fix != "proxy-login" || report.Error.Message != "not signed in to the team proxy: run proxy-login" {
		t.Errorf("a lapsed sign-in = exit %d, %+v; want exit 3 naming proxy-login", code, report.Error)
	}

	if err := runCLI([]string{"--context", "mystery", "whoami"}); exitOf(err) != exitAuth {
		t.Errorf("an auth no source provides = %v, want exit 3", err)
	}

	err := runCLI([]string{"--context", "redirect", "whoami"})
	if exitOf(err) != exitAuth || !strings.Contains(fmt.Sprint(err), "not signed in to 127.0.0.1: run `proxy-login`") {
		t.Errorf("a login redirect = %v, want exit 3 not signed in", err)
	}
	if _, _, fix := classifyError(err); fix != "run `proxy-login`" {
		t.Errorf("a login redirect's fix = %q", fix)
	}

	err = runCLI([]string{"--context", "html", "whoami"})
	if exitOf(err) != exitAuth || !strings.Contains(fmt.Sprint(err), "unexpected HTML sign-in response from "+api.URL+"/html") {
		t.Errorf("an HTML sign-in page = %v, want exit 3 unexpected HTML sign-in response", err)
	}
}
