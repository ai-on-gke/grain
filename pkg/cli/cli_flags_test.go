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
	"os"
	"path/filepath"
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

func TestAudienceDefault_EnvVars(t *testing.T) {
	t.Setenv("GRAIN_AUDIENCE", "")
	t.Setenv("GRAIN_TUNNEL_AUDIENCE", "")
	if got := audienceDefault(); got != "" {
		t.Fatalf("audienceDefault() with unset env = %q, want empty", got)
	}

	t.Setenv("GRAIN_TUNNEL_AUDIENCE", "https://tunnel.example.com/*")
	if got := audienceDefault(); got != "https://tunnel.example.com/*" {
		t.Fatalf("audienceDefault() with GRAIN_TUNNEL_AUDIENCE = %q, want https://tunnel.example.com/*", got)
	}

	t.Setenv("GRAIN_AUDIENCE", "https://iap.example.com/*")
	if got := audienceDefault(); got != "https://iap.example.com/*" {
		t.Fatalf("audienceDefault() with GRAIN_AUDIENCE = %q, want https://iap.example.com/*", got)
	}
}

func TestAsUserDefault_EnvVars(t *testing.T) {
	t.Setenv("GRAIN_USER", "")
	if got := asUserDefault(); got != "" {
		t.Fatalf("asUserDefault() with unset env = %q, want empty", got)
	}

	t.Setenv("GRAIN_USER", "  bob@example.com  ")
	if got := asUserDefault(); got != "bob@example.com" {
		t.Fatalf("asUserDefault() with GRAIN_USER = %q, want bob@example.com", got)
	}
}

func TestRunCLI_EnvVarDefaults(t *testing.T) {
	dir := t.TempDir()
	gcloudBin := filepath.Join(dir, "gcloud")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n  case \"$arg\" in\n    --audiences=*) printf 'tok:%s\\n' \"${arg#--audiences=}\"; exit 0 ;;\n  esac\ndone\nexit 1\n"
	if err := os.WriteFile(gcloudBin, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake gcloud: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(dir, "missing-adc.json"))
	t.Setenv("GRAIN_CLI_ENV", "")
	t.Setenv("GRAIN_DATA_DIR", "")
	t.Setenv("GRAIN_LOCAL_API_TOKEN_FILE", filepath.Join(dir, "missing-local-token"))
	for _, env := range []string{"GRAIN_TOKEN", "GRAIN_AUTH_TOKEN", "GRAIN_ID_TOKEN", "GRAIN_TUNNEL_TOKEN"} {
		t.Setenv(env, "")
	}

	var gotAuth, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUser = r.Header.Get("X-Forwarded-User")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	// Fallback to GRAIN_TUNNEL_AUDIENCE and GRAIN_USER when flags are omitted.
	t.Setenv("GRAIN_AUDIENCE", "")
	t.Setenv("GRAIN_TUNNEL_AUDIENCE", "https://tunnel.example.com")
	t.Setenv("GRAIN_USER", "carol@example.com")
	if err := runCLI([]string{"-server=" + srv.URL, "list"}); err != nil {
		t.Fatalf("runCLI with tunnel audience and user env failed: %v", err)
	}
	if gotAuth != "Bearer tok:https://tunnel.example.com" {
		t.Errorf("got Authorization %q, want Bearer tok:https://tunnel.example.com", gotAuth)
	}
	if gotUser != "carol@example.com" {
		t.Errorf("got X-Forwarded-User %q, want carol@example.com", gotUser)
	}

	// GRAIN_AUDIENCE takes precedence over GRAIN_TUNNEL_AUDIENCE.
	t.Setenv("GRAIN_AUDIENCE", "https://iap.example.com")
	if err := runCLI([]string{"-server=" + srv.URL, "list"}); err != nil {
		t.Fatalf("runCLI with GRAIN_AUDIENCE failed: %v", err)
	}
	if gotAuth != "Bearer tok:https://iap.example.com" {
		t.Errorf("got Authorization %q, want Bearer tok:https://iap.example.com", gotAuth)
	}

	// Explicit -audience and -as flags override the environment defaults.
	if err := runCLI([]string{
		"-server=" + srv.URL,
		"-audience=https://flag.example.com",
		"-as=dave@example.com",
		"list",
	}); err != nil {
		t.Fatalf("runCLI with explicit flags failed: %v", err)
	}
	if gotAuth != "Bearer tok:https://flag.example.com" {
		t.Errorf("got Authorization %q, want Bearer tok:https://flag.example.com", gotAuth)
	}
	if gotUser != "dave@example.com" {
		t.Errorf("got X-Forwarded-User %q, want dave@example.com", gotUser)
	}
}
