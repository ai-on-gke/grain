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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/hubenv"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestGrainHubBlamesTheHubWhenHubIsUnreachable covers the 2026-11-10 Grain Hub
// walkthrough (#3954): a `grain hub` subcommand dialing an unreachable hub must
// report the hub URL it dialled and how to change it (-hub-url / $GRAIN_HUB_URL),
// never the daemon's address or -server / GRAIN_SERVER, while subcommands that
// talk to the daemon (`status`, non-standalone `register`/`unregister`) keep
// the daemon wording when the daemon itself does not answer.
func TestGrainHubBlamesTheHubWhenHubIsUnreachable(t *testing.T) {
	writeCLIEnv(t, "")
	t.Setenv("GRAIN_HUB_URL", "")
	t.Setenv("GRAIN_REGISTRY_URL", "")
	prevLog := hubenv.Log
	hubenv.Log = func() io.Writer { return io.Discard }
	t.Cleanup(func() {
		hubenv.Log = prevLog
		hubenv.ResetForTest()
	})

	deadHub := closedServer(t)
	deadDaemon := closedServer(t)

	t.Run("default daemon address and explicit -hub-url", func(t *testing.T) {
		code, stdout, stderr := runForExit(t, []string{
			"-connect-timeout", "200ms",
			"hub", "list", "-hub-url", deadHub, "-hub-token", "x",
		})
		if code != exitUnreachable {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitUnreachable, stderr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
		for _, want := range []string{
			"no grain hub answered at " + deadHub,
			"Set $GRAIN_HUB_URL or pass -hub-url",
			"connection refused",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
		// Drop the dead hub's own URL before looking for daemon blame: its
		// ephemeral port can begin with 4242 (e.g. 127.0.0.1:42429), which
		// would otherwise match defaultServerURL as a substring.
		withoutHub := strings.ReplaceAll(stderr, deadHub, "<hub>")
		for _, wrong := range []string{"daemon", defaultServerURL, "GRAIN_SERVER", "-server"} {
			if strings.Contains(withoutHub, wrong) {
				t.Errorf("stderr = %q, must not blame %q", stderr, wrong)
			}
		}
	})

	t.Run("explicit -server and JSON error code", func(t *testing.T) {
		code, stdout, stderr := runForExit(t, []string{
			"-server", deadDaemon,
			"-connect-timeout", "200ms",
			"-json",
			"hub", "list", "-hub-url", deadHub, "-hub-token", "x",
		})
		if code != exitUnreachable {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitUnreachable, stderr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
		var got jsonError
		if err := json.Unmarshal([]byte(stderr), &got); err != nil {
			t.Fatalf("stderr = %q, want JSON error: %v", stderr, err)
		}
		if got.Error.Code != "hub_unreachable" {
			t.Errorf("error.code = %q, want \"hub_unreachable\"", got.Error.Code)
		}
		if !strings.Contains(got.Error.Message, deadHub) || strings.Contains(got.Error.Message, deadDaemon) {
			t.Errorf("error.message = %q, want hub URL %s and not daemon %s", got.Error.Message, deadHub, deadDaemon)
		}
	})

	t.Run("GRAIN_HUB_URL source is named", func(t *testing.T) {
		t.Setenv("GRAIN_HUB_URL", deadHub)
		code, _, stderr := runForExit(t, []string{
			"-connect-timeout", "200ms",
			"hub", "show", "-hub-token", "x", "my-entry",
		})
		if code != exitUnreachable {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitUnreachable, stderr)
		}
		want := "no grain hub answered at " + deadHub + " (from $GRAIN_HUB_URL)"
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("daemon settings hub URL source is named, not daemon address", func(t *testing.T) {
		st, _ := sqlitetest.Open(t)
		secStore := secrets.Open(secrets.Config{
			File:    t.TempDir() + "/secrets.age",
			KeyFile: t.TempDir() + "/secrets.key",
		})
		srv := ui.NewServer(withAccessCode(ui.Config{
			Actor:        ui.DefaultActor("operator@example.com"),
			Capabilities: ui.OfferedCapabilities(),
			Secrets:      secStore,
		}), st)
		daemonSrv := httptest.NewServer(srv)
		defer daemonSrv.Close()

		adminClient := accessCodeClient(daemonSrv.URL)
		initReq := settingsRequest("30s", 2, "github.example")
		hubCfg := model.HubConfig{URL: deadHub, Entry: "ws-1"}
		initReq.Hub = &hubCfg
		if _, err := adminClient.UpdateSettings(context.Background(), *initReq); err != nil {
			t.Fatalf("UpdateSettings: %v", err)
		}

		code, _, stderr := runForExit(t, []string{
			"-server", daemonSrv.URL,
			"-token", testAccessCode,
			"-connect-timeout", "200ms",
			"hub", "list", "-hub-token", "x",
		})
		if code != exitUnreachable {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitUnreachable, stderr)
		}
		want := "no grain hub answered at " + deadHub + " (from daemon settings)"
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		if strings.Contains(stderr, daemonSrv.URL) {
			t.Errorf("stderr = %q, must not blame live daemon %s", stderr, daemonSrv.URL)
		}

		// Non-standalone unregister against a live daemon and dead hub keeps
		// the -keep-remote guidance while still classifying as hub_unreachable.
		code, _, stderr = runForExit(t, []string{
			"-server", daemonSrv.URL,
			"-token", testAccessCode,
			"-connect-timeout", "200ms",
			"hub", "unregister", "-hub-token", "oidc-tok",
		})
		if code != exitUnreachable {
			t.Fatalf("unregister exit = %d, want %d (stderr=%q)", code, exitUnreachable, stderr)
		}
		for _, sub := range []string{"deleting ws-1 on hub " + deadHub, "-keep-remote", "no grain hub answered at " + deadHub} {
			if !strings.Contains(stderr, sub) {
				t.Errorf("unregister stderr = %q, want %q", stderr, sub)
			}
		}
	})

	t.Run("timeout against hanging hub blames the hub", func(t *testing.T) {
		release := make(chan struct{})
		hangingHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
		}))
		defer func() {
			close(release)
			hangingHub.Close()
		}()

		start := time.Now()
		code, _, stderr := runForExit(t, []string{
			"-connect-timeout", "200ms",
			"-timeout", "250ms",
			"hub", "list", "-hub-url", hangingHub.URL, "-hub-token", "x",
		})
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("hanging hub took %s with -timeout 250ms", elapsed)
		}
		if code != exitUnreachable {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitUnreachable, stderr)
		}
		if !strings.Contains(stderr, "the grain hub at "+hangingHub.URL+" did not answer in time") {
			t.Errorf("stderr = %q, want hub timeout message", stderr)
		}
		if strings.Contains(stderr, "daemon") {
			t.Errorf("stderr = %q, must not mention daemon", stderr)
		}
	})

	t.Run("standalone register and unregister against dead hub blame the hub", func(t *testing.T) {
		for _, subArgs := range [][]string{
			{"hub", "register", "-standalone", "-hub-url", deadHub, "-hub-token", "oidc-tok", "ws-1"},
			{"hub", "unregister", "-standalone", "-hub-url", deadHub, "-hub-token", "oidc-tok", "ws-1"},
		} {
			code, _, stderr := runForExit(t, append([]string{"-connect-timeout", "200ms"}, subArgs...))
			if code != exitUnreachable {
				t.Fatalf("%v: exit = %d, want %d (stderr=%q)", subArgs, code, exitUnreachable, stderr)
			}
			if !strings.Contains(stderr, "no grain hub answered at "+deadHub) || strings.Contains(stderr, "daemon") {
				t.Errorf("%v: stderr = %q, want hub unreachable without daemon wording", subArgs, stderr)
			}
		}
	})

	t.Run("daemon-talking subcommands keep daemon wording", func(t *testing.T) {
		for _, subArgs := range [][]string{
			{"hub", "status"},
			{"hub", "register", "-hub-url", "https://hub.example.com", "ws-1"},
			{"hub", "unregister", "ws-1"},
		} {
			code, _, stderr := runForExit(t, append([]string{"-server", deadDaemon, "-connect-timeout", "200ms"}, subArgs...))
			if code != exitUnreachable {
				t.Fatalf("%v: exit = %d, want %d (stderr=%q)", subArgs, code, exitUnreachable, stderr)
			}
			for _, want := range []string{"no grain daemon answered at " + deadDaemon, "Set GRAIN_SERVER or pass -server"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("%v: stderr = %q, want %q", subArgs, stderr, want)
				}
			}
		}
	})
}

func TestGrainHubUserVisibleErrorsSayHubNotRegistry(t *testing.T) {
	// 1. checkHubConfig validation error says "hub configuration" and "hub URL".
	err := checkHubConfig(daemon.Config{
		Hub: model.HubConfig{
			URL:   "http://insecure.example.com",
			Entry: "ws-1",
		},
	})
	if err == nil {
		t.Fatal("checkHubConfig accepted insecure non-loopback http URL")
	}
	for _, want := range []string{"hub configuration:", "hub url:", "hub URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("checkHubConfig error = %q, want %q", err.Error(), want)
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "registry") {
		t.Errorf("checkHubConfig error still mentions registry: %q", err.Error())
	}

	// 2. Failing hub API responses say "hub API" and "hub store", not "registry".
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":  "quota_exceeded",
			"error": "hub store: quota exceeded",
		})
	}))
	defer hubSrv.Close()

	code, _, stderr := runForExit(t, []string{
		"hub", "create", "-hub-url", hubSrv.URL, "-hub-token", "oidc-tok", "quota-vm",
	})
	if code != exitAuth {
		t.Fatalf("quota 403 exit = %d, want %d (stderr=%q)", code, exitAuth, stderr)
	}
	for _, want := range []string{"hub API 403 (quota_exceeded)", "hub store: quota exceeded"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if strings.Contains(strings.ToLower(stderr), "registry") {
		t.Errorf("stderr still mentions registry: %q", stderr)
	}

	// 3. `grain hub unregister` with nothing configured says "hub configuration cleared".
	st, _ := sqlitetest.Open(t)
	secStore := secrets.Open(secrets.Config{
		File:    t.TempDir() + "/secrets.age",
		KeyFile: t.TempDir() + "/secrets.key",
	})
	daemonSrv := httptest.NewServer(ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator@example.com"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secStore,
	}), st))
	defer daemonSrv.Close()
	if _, err := accessCodeClient(daemonSrv.URL).UpdateSettings(context.Background(), *settingsRequest("30s", 2, "github.example")); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runForExit(t, []string{
		"-server", daemonSrv.URL,
		"-token", testAccessCode,
		"hub", "unregister",
	})
	if code != exitOK {
		t.Fatalf("unregister exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "hub configuration cleared" {
		t.Errorf("unregister stdout = %q, want \"hub configuration cleared\"", stdout)
	}

	// 4. Unknown subcommands say "unknown hub ... subcommand".
	for _, args := range [][]string{
		{"hub", "nope"},
		{"hub", "delegating-apps", "nope"},
		{"hub", "grants", "nope"},
	} {
		_, _, subErr := runForExit(t, args)
		if !strings.Contains(subErr, "unknown hub ") || strings.Contains(strings.ToLower(subErr), "unknown registry") {
			t.Errorf("%v stderr = %q, want \"unknown hub ...\"", args, subErr)
		}
	}
}
