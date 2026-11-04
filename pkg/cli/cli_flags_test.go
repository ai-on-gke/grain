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
