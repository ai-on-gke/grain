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

package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// hostdInstallVariants are the script as the daemon serves it with
// everything filled in, with nothing, and the embedded file itself.
func hostdInstallVariants() map[string]string {
	return map[string]string{
		"filled":   renderHostdInstallScript("https://grain.example.com:8420", "v2.3.0-dev.114+abc1234", "ghcr.io/example/grain:main"),
		"empty":    renderHostdInstallScript("", "", ""),
		"embedded": hostdInstallScript,
	}
}

func TestHostdInstallScriptParses(t *testing.T) {
	for name, script := range hostdInstallVariants() {
		path := filepath.Join(t.TempDir(), "install.sh")
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s: sh -n: %v\n%s", name, err, out)
		}
	}
}

func TestHostdInstallScriptPassesShellcheck(t *testing.T) {
	shellcheck, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck is not installed")
	}
	for name, script := range hostdInstallVariants() {
		path := filepath.Join(t.TempDir(), "install.sh")
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(shellcheck, "-s", "sh", path).CombinedOutput(); err != nil {
			t.Errorf("%s: shellcheck: %v\n%s", name, err, out)
		}
	}
}

func TestHostdInstallScriptHelpRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(hostdInstallVariants()["filled"]), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", path, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("sh install.sh --help: %v\n%s", err, out)
	}
	for _, want := range []string{"https://grain.example.com:8420", "ghcr.io/example/grain:main", "--name NAME"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("--help does not say %q:\n%s", want, out)
		}
	}
}

func TestHostdInstallScriptNamesTheRequestingDaemon(t *testing.T) {
	for _, tc := range []struct {
		name, host, proto string
		tls               bool
		want              string
	}{
		{name: "plain", host: "grain.example.com:8420", want: "GRAIN_DAEMON_URL='http://grain.example.com:8420'"},
		{name: "behind a TLS proxy", host: "grain.example.com", proto: "https", want: "GRAIN_DAEMON_URL='https://grain.example.com'"},
		{name: "IPv6", host: "[::1]:8420", want: "GRAIN_DAEMON_URL='http://[::1]:8420'"},
		// A Host the script could not carry is left out, for --server.
		{name: "quote", host: "evil'$(id)", want: "GRAIN_DAEMON_URL=''"},
		{name: "space", host: "a b", want: "GRAIN_DAEMON_URL=''"},
		{name: "path", host: "grain.example.com/x", want: "GRAIN_DAEMON_URL=''"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, hostdInstallPath, nil)
			r.Host = tc.host
			if tc.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			w := httptest.NewRecorder()
			(&Server{}).handleHostdInstallScript(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
				t.Errorf("Content-Type %q", ct)
			}
			body := w.Body.String()
			if !strings.Contains(body, tc.want) {
				t.Errorf("script does not hold %s", tc.want)
			}
			if strings.Contains(body, "@GRAIN_DAEMON_") {
				t.Error("script still holds a placeholder")
			}
		})
	}
}

func TestRenderHostdInstallScriptDropsWhatItCannotCarry(t *testing.T) {
	script := renderHostdInstallScript("https://grain.example.com", "v1'; rm -rf /", "img:$(id)")
	for _, want := range []string{"GRAIN_DAEMON_URL='https://grain.example.com'", "GRAIN_DAEMON_RELEASE=''", "GRAIN_DAEMON_IMAGE=''"} {
		if !strings.Contains(script, want) {
			t.Errorf("script does not hold %s", want)
		}
	}
}

func TestHostdInstallRoute(t *testing.T) {
	for target, want := range map[string]bool{
		"/api/hostd/install.sh":   true,
		"/api/hostd/install.sh/x": false,
		"/api/hostd/installsh":    false,
		"/api/hostds":             false,
	} {
		r := httptest.NewRequest(http.MethodGet, "http://grain.example/", nil)
		r.URL.Path = target
		if got := hostdInstallRoute(r); got != want {
			t.Errorf("GET %s: hostdInstallRoute = %v, want %v", target, got, want)
		}
		if got := requiresAccess(r); got == want {
			t.Errorf("GET %s: requiresAccess = %v", target, got)
		}
		r.Method = http.MethodPost
		if hostdInstallRoute(r) {
			t.Errorf("POST %s counted as the install script", target)
		}
	}
}

// runningImageUpgrader is an Upgrader that says only which image it runs.
type runningImageUpgrader struct{ ref string }

func (runningImageUpgrader) Start(string) error                      { return nil }
func (runningImageUpgrader) ValidateBranch(b string) (string, error) { return b, nil }
func (runningImageUpgrader) Status() (upgrade.Status, error)         { return upgrade.Status{}, nil }
func (runningImageUpgrader) Mode() upgrade.Mode                      { return upgrade.ModeImage }
func (u runningImageUpgrader) Running() (upgrade.Running, error) {
	return upgrade.Running{Ref: u.ref}, nil
}

// TestHostdInstallScriptTellsOnlyACallerWithAccessTheImage: the script
// answers anybody, but the daemon's release and the image it runs are
// filled in only for a caller the daemon trusts -- an in-process one
// here -- and left blank for a stranger, who gets them from the command
// Settings -> Hostds shows (HostdInstall).
func TestHostdInstallScriptTellsOnlyACallerWithAccessTheImage(t *testing.T) {
	const image = "registry.example.com/private/grain:v1.2.3"
	s := &Server{tasks: &Client{Config: Config{Upgrader: runningImageUpgrader{ref: image}}}}
	for _, tc := range []struct {
		name, from string
		want       string
	}{
		{name: "stranger", from: "203.0.113.9:4242", want: "GRAIN_DAEMON_IMAGE=''"},
		{name: "in-process", from: "", want: "GRAIN_DAEMON_IMAGE='" + image + "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, hostdInstallPath, nil)
			r.RemoteAddr = tc.from
			w := httptest.NewRecorder()
			s.handleHostdInstallScript(w, r)
			body := w.Body.String()
			if !strings.Contains(body, tc.want) {
				t.Errorf("script does not hold %s", tc.want)
			}
			if tc.from != "" && !strings.Contains(body, "GRAIN_DAEMON_RELEASE=''") {
				t.Error("a stranger's script names the daemon's release")
			}
		})
	}
	if got := s.hostdInstallInfo(); got.Image != image {
		t.Errorf("hostdInstallInfo().Image = %q, want %q", got.Image, image)
	}
}

// TestHostdInstallScriptRefusesAnUnknownVersion: a copy of the script
// that was not told the daemon's version installs nothing rather than
// taking whatever grain it finds, unless told --release or --any-version.
func TestHostdInstallScriptRefusesAnUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(renderHostdInstallScript("https://grain.example.com", "", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	// check_request's version check comes after the root check, so this
	// reads the script for it rather than running it as root.
	script, _ := os.ReadFile(path)
	if !strings.Contains(string(script), `[ -n "$RELEASE" ] || die "this copy of the script does not know the daemon's version`) {
		t.Error("the script no longer refuses to install without the daemon's version")
	}
	out, err := exec.Command("sh", path, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--release VERSION") {
		t.Errorf("--help does not offer --release:\n%s", out)
	}
}
