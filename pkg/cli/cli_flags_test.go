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
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunCLI_GlobalFlags(t *testing.T) {
	var gotAuth, gotTunnel, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTunnel = r.Header.Get("X-Grain-Tunnel-Token")
		gotUser = r.Header.Get("X-Forwarded-User")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	err := runCLI([]string{
		"-server=" + srv.URL,
		"-token=my-secret-token",
		"-as=alice@example.com",
		"list",
	})
	if err != nil {
		t.Fatalf("runCLI failed: %v", err)
	}

	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("got Authorization %q, want Bearer my-secret-token", gotAuth)
	}
	if gotTunnel != "my-secret-token" {
		t.Errorf("got X-Grain-Tunnel-Token %q, want my-secret-token", gotTunnel)
	}
	if gotUser != "alice@example.com" {
		t.Errorf("got X-Forwarded-User %q, want alice@example.com", gotUser)
	}
}

func TestRunCLI_UsageIncludesNewFlags(t *testing.T) {
	for _, flag := range []string{"-token", "-audience", "-as", "-server"} {
		if !strings.Contains(usage, flag) {
			t.Errorf("usage missing expected flag %q", flag)
		}
	}
}

// #XC-11: the global flags mean the same before the verb and after it,
// with one dash or two, and after a subcommand's own arguments too.
func TestGlobalFlagsAcceptedBeforeAndAfterVerb(t *testing.T) {
	var gotAuth, gotUser, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUser = r.Header.Get("X-Forwarded-User")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/ci/runs/") {
			_, _ = w.Write([]byte(`{"id":"ci-42","status":"completed","conclusion":"success"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name     string
		args     []string
		wantPath string
	}{
		{"before, one dash", []string{"-server", srv.URL, "-token", "tok", "-as", "alice@example.com", "-json", "list"}, "/api/tasks"},
		{"before, two dashes", []string{"--server", srv.URL, "--token=tok", "--as=alice@example.com", "--json", "list"}, "/api/tasks"},
		{"after, one dash", []string{"list", "-server", srv.URL, "-token", "tok", "-as", "alice@example.com", "-json"}, "/api/tasks"},
		{"after, two dashes", []string{"list", "--server=" + srv.URL, "--token", "tok", "--as", "alice@example.com", "--json"}, "/api/tasks"},
		{"split across the verb", []string{"--server", srv.URL, "list", "--token", "tok", "-as=alice@example.com", "--json", "--connect-timeout", "2s", "--timeout=30s"}, "/api/tasks"},
		{"after a subcommand's argument", []string{"ci", "show", "ci-42", "--json", "--server", srv.URL, "--token", "tok", "--as", "alice@example.com"}, "/api/ci/runs/ci-42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotAuth, gotUser, gotPath = "", "", ""
			var err error
			out := captureStdout(t, func() { err = runCLI(tc.args) })
			if err != nil {
				t.Fatalf("grain %s: %v", strings.Join(tc.args, " "), err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("asked for %q, want %q -- -server not honoured", gotPath, tc.wantPath)
			}
			if gotAuth != "Bearer tok" {
				t.Errorf("Authorization = %q, want Bearer tok", gotAuth)
			}
			if gotUser != "alice@example.com" {
				t.Errorf("X-Forwarded-User = %q, want alice@example.com", gotUser)
			}
			var v any
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Errorf("printed %q, want JSON: -json not honoured", out)
			}
		})
	}

	// A word nobody reads is still refused, with exit 2, and a global
	// flag's missing value is a usage error rather than a panic.
	for _, args := range [][]string{
		{"-server", srv.URL, "list", "--json", "junk"},
		{"-server", srv.URL, "list", "--server"},
		{"-server", srv.URL, "ci", "show", "ci-42", "extra", "--json"},
	} {
		var errOut strings.Builder
		var code int
		captureStdout(t, func() { code = runTaskCLI(args, &errOut) })
		if code != exitUsage {
			t.Errorf("grain %s = exit %d, want 2 (stderr %q)", strings.Join(args, " "), code, errOut.String())
		}
	}

	// A comment's body is its own: a flag-shaped word in it is not lifted.
	kept, err := liftGlobalFlags(flag.NewFlagSet("grain", flag.ContinueOnError), "comment", []string{"5", "file", "this", "-as", "a", "bug"})
	if err != nil || strings.Join(kept, " ") != "5 file this -as a bug" {
		t.Errorf("liftGlobalFlags(comment ...) = %v, %v; want the body left alone", kept, err)
	}
}
