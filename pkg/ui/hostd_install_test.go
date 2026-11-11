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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	agentdrelease "github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/model"
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
	for _, tc := range []struct {
		release, image string
	}{
		{release: "v1'; rm -rf /", image: "img:$(id)"},
		{release: "-v1.2.3", image: "--help"},
	} {
		script := renderHostdInstallScript("https://grain.example.com", tc.release, tc.image)
		for _, want := range []string{"GRAIN_DAEMON_URL='https://grain.example.com'", "GRAIN_DAEMON_RELEASE=''", "GRAIN_DAEMON_IMAGE=''"} {
			if !strings.Contains(script, want) {
				t.Errorf("release=%q image=%q: script does not hold %s", tc.release, tc.image, want)
			}
		}
	}
}

func TestHostdInstallRoute(t *testing.T) {
	for target, want := range map[string]bool{
		"/api/hostd/install.sh":                       true,
		"/api/hostd/agentd/linux/amd64":               true,
		"/api/hostd/agentd/linux/amd64/manifest.json": true,
		"/api/hostd/agentd/linux/arm64":               true,
		"/api/hostd/agentd/linux/arm64/manifest.json": true,
		"/api/hostd/agentd/linux":                     false,
		"/api/hostd/agentd/linux/amd64/extra":         false,
		"/api/hostd/install.sh/x":                     false,
		"/api/hostd/installsh":                        false,
		"/api/hostds":                                 false,
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
	// --join is no exception: the invite snippet says --release or
	// --any-version itself (defaultHostInviteInstall), in sight.
	for _, line := range strings.Split(string(script), "\n") {
		if strings.Contains(line, "ANY_VERSION=1") && !strings.Contains(line, "--any-version)") {
			t.Errorf("the script turns --any-version on by itself: %q", strings.TrimSpace(line))
		}
	}
	out, err := exec.Command("sh", path, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--release VERSION") {
		t.Errorf("--help does not offer --release:\n%s", out)
	}
}

// TestHostdInstallScriptRateLimitsBearerAccessCodeGuesses: wrong bearer
// access codes sent to /api/hostd/install.sh count towards s.access, and a
// remote source over its limit is told neither the release nor the image
// even when it sends the right code, while an unauthenticated fetch from
// the same source still gets the public script.
func TestHostdInstallScriptRateLimitsBearerAccessCodeGuesses(t *testing.T) {
	const (
		code      = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
		wrong     = "WRNG-WRNG-WRNG-WRNG-WRNG-WRNG-WRNG-WRNG"
		image     = "registry.example.com/private/grain:v1.2.3"
		guesser   = "203.0.113.9:4242"
		bystander = "198.51.100.200:4242"
	)
	srv := NewServer(Config{LocalAPIToken: code, Upgrader: runningImageUpgrader{ref: image}}, nil)
	fetch := func(from, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://grain.example.com"+hostdInstallPath, nil)
		r.RemoteAddr = from
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}

	for i := 0; i < accessSourceBurst+2; i++ {
		w := fetch(guesser, wrong)
		if w.Code != http.StatusOK {
			t.Fatalf("guess %d: status %d, want 200", i+1, w.Code)
		}
		if !strings.Contains(w.Body.String(), "GRAIN_DAEMON_IMAGE=''") {
			t.Fatalf("guess %d: wrong code revealed image", i+1)
		}
	}

	// Guesses on /api/hostd/install.sh spent the source's access-code
	// bucket, so login and API routes now answer 429 from that source.
	loginReq := httptest.NewRequest(http.MethodPost, "http://grain.example.com/api/auth/access-code", strings.NewReader(`{"code":"`+code+`"}`))
	loginReq.RemoteAddr = guesser
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	srv.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusTooManyRequests {
		t.Errorf("POST /api/auth/access-code from exhausted source: %d, want 429", loginRec.Code)
	}

	// Nor is /api/hostd/install.sh an oracle once limited: the right code
	// from that source gets the same blank image as a wrong one.
	if w := fetch(guesser, code); !strings.Contains(w.Body.String(), "GRAIN_DAEMON_IMAGE=''") {
		t.Error("the right bearer code from a rate-limited source revealed the image")
	}
	// A plain curl with no bearer from the same source still gets 200.
	if w := fetch(guesser, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "GRAIN_DAEMON_IMAGE=''") {
		t.Errorf("unauthenticated fetch from rate-limited source: %d", w.Code)
	}
	// Another source (and loopback) with the right bearer code still gets
	// the image.
	for _, from := range []string{bystander, "127.0.0.1:4242"} {
		if w := fetch(from, code); !strings.Contains(w.Body.String(), "GRAIN_DAEMON_IMAGE='"+image+"'") {
			t.Errorf("the right bearer code from %s did not include the image", from)
		}
	}
}

// TestHostdInstallScriptRefusesUnsafeInputs exercises check_request against
// leading-hyphen option values, non-HTTP binary URLs, and malformed SHA-256
// digests (including a newline value that command substitution strips).
func TestHostdInstallScriptRefusesUnsafeInputs(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(scriptPath, []byte(renderHostdInstallScript("https://grain.example.com", "v2.3.0", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub := func(name, body string) {
		p := filepath.Join(binDir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Pretend to be root on a systemd Linux box with non-root user "hostd".
	writeStub("id", `if [ "$1" = "-u" ] && [ $# -eq 1 ]; then echo 0; elif [ "$1" = "-u" ]; then echo 1000; elif [ "$1" = "-gn" ]; then echo hostd; fi`)
	writeStub("uname", `echo Linux`)
	writeStub("systemctl", `exit 0`)

	for _, tc := range []struct {
		name string
		args []string
		env  []string
		want string
	}{
		{name: "leading hyphen release", args: []string{"--name", "box", "--user", "hostd", "--release", "-v"}, want: "not a grain version"},
		{name: "leading hyphen image", args: []string{"--name", "box", "--user", "hostd", "--image", "--help"}, want: "not an image reference"},
		{name: "file scheme binary-url", args: []string{"--name", "box", "--user", "hostd", "--binary-url", "file:///etc/passwd", "--sha256", strings.Repeat("a", 64)}, want: "not an http:// or https:// URL"},
		{name: "leading hyphen binary-url", args: []string{"--name", "box", "--user", "hostd", "--binary-url", "-K/tmp/curlrc", "--sha256", strings.Repeat("a", 64)}, want: "not an http:// or https:// URL"},
		{name: "short sha256", args: []string{"--name", "box", "--user", "hostd", "--binary-url", "https://grain.example.com/grain", "--sha256", "deadbeef"}, want: "64-digit hex SHA-256"},
		{name: "newline sha256 env", args: []string{"--name", "box", "--user", "hostd", "--binary-url", "https://grain.example.com/grain"}, env: []string{"GRAIN_BINARY_SHA256=\n"}, want: "64-digit hex SHA-256"},
		{name: "non-hex sha256", args: []string{"--name", "box", "--user", "hostd", "--sha256", strings.Repeat("z", 64)}, want: "64-digit hex SHA-256"},
		{name: "leading hyphen iap-audience", args: []string{"--name", "box", "--user", "hostd", "--iap-audience", "-bad"}, want: "leading '-'"},
		{name: "leading hyphen ca-file", args: []string{"--name", "box", "--user", "hostd", "--ca-file", "-bad"}, want: "leading '-'"},
		{name: "leading hyphen join", args: []string{"--join", "-bad", "--user", "hostd"}, want: "not a valid enrollment code"},
		{name: "shell metachar join", args: []string{"--join", "ghe_abc;id", "--user", "hostd"}, want: "not a valid enrollment code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", append([]string{scriptPath}, tc.args...)...)
			cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "GRAIN_HOSTD_TOKEN=ghd_testtoken")
			cmd.Env = append(cmd.Env, tc.env...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected failure containing %q, got exit 0:\n%s", tc.want, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("output = %q, want substring %q", out, tc.want)
			}
		})
	}
}

// TestHostdInstallForDropsInstructionsCarryingTheToken: a provider is
// never given the token, but instructions that carry it anyway -- however
// it reached them -- are dropped for the default's, and the default's are
// dropped too rather than shown with it.
func TestHostdInstallForDropsInstructionsCarryingTheToken(t *testing.T) {
	const token = "ghd_example-token-0123456789"
	s := &Server{tasks: &Client{}}
	r := httptest.NewRequest(http.MethodPost, "http://grain.example.com/api/hostds", nil)
	h := model.Hostd{Name: "ws-1", Owner: "alice@example.com"}

	t.Cleanup(RegisterHostdInstall(func(c HostdInstallContext) HostdInstallInstructions {
		return HostdInstallInstructions{Lines: []string{"GRAIN_HOSTD_TOKEN=" + token + " ./install.sh"}}
	}))
	got := s.hostdInstallFor(r, h, token)
	if len(got.Lines) != 1 || !strings.HasPrefix(got.Lines[0], "sudo sh -c") || strings.Contains(got.Lines[0], token) {
		t.Errorf("lines carrying the token gave %q, want the default line", got.Lines)
	}

	t.Cleanup(RegisterHostdInstall(func(c HostdInstallContext) HostdInstallInstructions {
		return HostdInstallInstructions{Lines: []string{"./install.sh"}, Note: "the token is " + token}
	}))
	if got := s.hostdInstallFor(r, h, token); got.Note != "" || len(got.Lines) != 1 || got.Lines[0] == "./install.sh" {
		t.Errorf("a note carrying the token gave %+v, want the default", got)
	}

	// A hostd named like its token (it cannot be: tokens are not valid
	// names) would put it in the default line; nothing is shown then.
	unregister := RegisterHostdInstall(nil)
	defer unregister()
	if got := s.hostdInstallFor(r, model.Hostd{Name: token}, token); len(got.Lines) != 0 {
		t.Errorf("a default line carrying the token was shown: %q", got.Lines)
	}
}

// TestDefaultHostdInstall: the root installer's line, with the daemon's
// release and image when it has them, and no line without an origin.
func TestDefaultHostdInstall(t *testing.T) {
	for _, tc := range []struct {
		c    HostdInstallContext
		want []string
	}{
		{c: HostdInstallContext{Origin: "https://grain.example.com", Name: "ws-1"},
			want: []string{`sudo sh -c "$(curl -fsSL 'https://grain.example.com/api/hostd/install.sh')" grain-hostd-install --name ws-1 --any-version`}},
		{c: HostdInstallContext{Origin: "http://127.0.0.1:8080", Name: "ws-1", Release: "v1.2.3", Image: "registry.example.com/grain:v1.2.3"},
			want: []string{`sudo sh -c "$(curl -fsSL 'http://127.0.0.1:8080/api/hostd/install.sh')" grain-hostd-install --name ws-1 --release v1.2.3 --image registry.example.com/grain:v1.2.3`}},
		{c: HostdInstallContext{Name: "ws-1"}},
	} {
		if got := defaultHostdInstall(tc.c); strings.Join(got.Lines, "\n") != strings.Join(tc.want, "\n") || got.Note != "" {
			t.Errorf("defaultHostdInstall(%+v) = %+v, want %q", tc.c, got, tc.want)
		}
	}
}

// TestHostdInstallOrigin: an origin a shell line cannot carry between
// single quotes is left out.
func TestHostdInstallOrigin(t *testing.T) {
	for host, want := range map[string]string{
		"grain.example.com":      "http://grain.example.com",
		"grain.example.com:8443": "http://grain.example.com:8443",
		"[::1]:8080":             "http://[::1]:8080",
		"evil.example.com'$(id)": "",
		"":                       "",
	} {
		r := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
		r.Host = host
		if got := hostdInstallOrigin(r); got != want {
			t.Errorf("Host %q: hostdInstallOrigin = %q, want %q", host, got, want)
		}
	}
}

// TestDefaultHostInviteInstall: the shared --join line, which -- like the
// per-host one -- names the daemon's release, or says --any-version when
// the daemon cannot, rather than leaving the script to skip the check.
func TestDefaultHostInviteInstall(t *testing.T) {
	for _, tc := range []struct {
		c    HostdInstallContext
		want []string
	}{
		{c: HostdInstallContext{Origin: "https://grain.example.com", JoinCode: "ghe_abc"},
			want: []string{"curl -fsSL https://grain.example.com/hostd/install | sh -s -- --join ghe_abc --any-version"}},
		{c: HostdInstallContext{Origin: "https://grain.example.com", JoinCode: "ghe_abc", Release: "v1.2.3", Image: "registry.example.com/grain:v1.2.3"},
			want: []string{"curl -fsSL https://grain.example.com/hostd/install | sh -s -- --join ghe_abc --release v1.2.3 --image registry.example.com/grain:v1.2.3"}},
		{c: HostdInstallContext{JoinCode: "ghe_abc"}},
		{c: HostdInstallContext{Origin: "https://grain.example.com"}},
	} {
		if got := defaultHostInviteInstall(tc.c); strings.Join(got.Lines, "\n") != strings.Join(tc.want, "\n") || got.Note != "" {
			t.Errorf("defaultHostInviteInstall(%+v) = %+v, want %q", tc.c, got, tc.want)
		}
	}
}

func TestControllerServesSignedAgentdBinaryAndManifestOffline(t *testing.T) {
	pub, priv, err := agentdrelease.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := agentdrelease.FormatPublicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}
	distDir := t.TempDir()
	t.Setenv(agentdDistDirEnv, distDir)
	t.Setenv(agentdReleaseURLEnv, "off")
	t.Setenv(agentdReleasePublicKeyEnv, string(pubPEM))

	binPayload := []byte("#!/bin/sh\nprintf 'agentd args: %s\\n' \"$*\"\n")
	m, err := agentdrelease.NewManifestForBinary("v3.5.0", "linux/amd64", binPayload, nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := agentdrelease.SignManifest(m, priv, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "grain-agentd-linux-amd64"), binPayload, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "grain-agentd-linux-amd64.manifest.json"), manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(Config{LocalAPIToken: "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"}, nil)

	// Open without the single-tenant access code, for hosts fetching over TLS.
	mReq := httptest.NewRequest(http.MethodGet, "https://grain.example.com/api/hostd/agentd/linux/amd64/manifest.json", nil)
	mReq.RemoteAddr = "203.0.113.44:50000"
	mRec := httptest.NewRecorder()
	srv.ServeHTTP(mRec, mReq)
	if mRec.Code != http.StatusOK {
		t.Fatalf("GET manifest = %d: %s", mRec.Code, mRec.Body.String())
	}
	gotManifest, err := agentdrelease.ParseManifest(mRec.Body.Bytes())
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	bReq := httptest.NewRequest(http.MethodGet, "https://grain.example.com/api/hostd/agentd/linux/amd64", nil)
	bReq.RemoteAddr = "203.0.113.44:50000"
	bRec := httptest.NewRecorder()
	srv.ServeHTTP(bRec, bReq)
	if bRec.Code != http.StatusOK {
		t.Fatalf("GET binary = %d: %s", bRec.Code, bRec.Body.String())
	}
	if !bytes.Equal(bRec.Body.Bytes(), binPayload) {
		t.Fatalf("binary payload mismatch")
	}
	if _, err := agentdrelease.VerifyBinary(gotManifest, bRec.Body.Bytes(), pub); err != nil {
		t.Fatalf("VerifyBinary against pinned key: %v", err)
	}

	// Unsupported platform returns 404.
	dReq := httptest.NewRequest(http.MethodGet, "https://grain.example.com/api/hostd/agentd/darwin/arm64", nil)
	dReq.RemoteAddr = "203.0.113.44:50000"
	dRec := httptest.NewRecorder()
	srv.ServeHTTP(dRec, dReq)
	if dRec.Code != http.StatusNotFound {
		t.Errorf("GET darwin/arm64 = %d, want 404", dRec.Code)
	}

	// End-to-end check: hostd_install.sh --agentd downloads from controller when
	// grain-agentd is absent from $PATH and ~/.local/bin.
	if runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		archAsset := "grain-agentd-linux-" + runtime.GOARCH
		mArch, _ := agentdrelease.NewManifestForBinary("v3.5.0", "linux/"+runtime.GOARCH, binPayload, nil)
		sArch, _ := agentdrelease.SignManifest(mArch, priv, nil)
		rawArch, _ := json.MarshalIndent(sArch, "", "  ")
		_ = os.WriteFile(filepath.Join(distDir, archAsset), binPayload, 0o755)
		_ = os.WriteFile(filepath.Join(distDir, archAsset+".manifest.json"), rawArch, 0o644)

		ts := httptest.NewServer(srv)
		defer ts.Close()

		scriptReq := httptest.NewRequest(http.MethodGet, ts.URL+"/hostd/install", nil)
		scriptRec := httptest.NewRecorder()
		srv.ServeHTTP(scriptRec, scriptReq)
		scriptPath := filepath.Join(t.TempDir(), "install.sh")
		if err := os.WriteFile(scriptPath, scriptRec.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		fakeHome := t.TempDir()
		emptyPath := t.TempDir()
		// Symlink sh/curl/tr/sed/sha256sum/shasum/chmod/mkdir/mktemp/id/uname/rm so
		// PATH holds no pre-existing grain-agentd.
		for _, tool := range []string{"sh", "curl", "wget", "tr", "sed", "sha256sum", "shasum", "awk", "chmod", "mkdir", "mktemp", "id", "uname", "rm", "cat", "head"} {
			if p, err := exec.LookPath(tool); err == nil {
				_ = os.Symlink(p, filepath.Join(emptyPath, tool))
			}
		}
		cmd := exec.Command("sh", scriptPath, "--agentd", "--server", ts.URL, "--name", "ws-offline", "--no-start")
		cmd.Env = []string{"HOME=" + fakeHome, "PATH=" + emptyPath}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sh install.sh --agentd: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "agentd args: install -server "+ts.URL+" -name ws-offline -no-start") {
			t.Errorf("unexpected output from downloaded agentd:\n%s", out)
		}
	}
}

func TestControllerFetchesAndCachesSignedAgentdFromReleaseURL(t *testing.T) {
	pub, priv, _ := agentdrelease.GenerateKeyPair()
	pubPEM, _ := agentdrelease.FormatPublicKeyPEM(pub)

	binPayload := []byte("upstream-signed-agentd-binary")
	const rel = "v9.4.2"
	m, _ := agentdrelease.NewManifestForBinary(rel, "linux/arm64", binPayload, nil)
	signed, _ := agentdrelease.SignManifest(m, priv, nil)
	rawManifest, _ := json.Marshal(signed)

	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/releases/" + rel + "/grain-agentd-linux-arm64.manifest.json":
			_, _ = w.Write(rawManifest)
		case "/releases/" + rel + "/grain-agentd-linux-arm64":
			_, _ = w.Write(binPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	t.Setenv(agentdDistDirEnv, t.TempDir()) // empty local dir
	t.Setenv(agentdReleaseURLEnv, upstream.URL+"/releases")
	t.Setenv(agentdReleasePublicKeyEnv, string(pubPEM))
	oldRel := cliInstallRelease
	cliInstallRelease = func() string { return rel }
	t.Cleanup(func() { cliInstallRelease = oldRel })

	srv := NewServer(Config{}, nil)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "https://grain.example.com/api/hostd/agentd/linux/arm64", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), binPayload) {
			t.Fatalf("iter %d: code=%d body=%q", i, rec.Code, rec.Body.String())
		}
	}
	if hits != 2 {
		t.Errorf("upstream hit %d times across 2 binary requests, want 2 (1 manifest + 1 binary, cached thereafter)", hits)
	}
}

func TestControllerRefusesTamperedLocalAgentdManifestSignature(t *testing.T) {
	pub, _, _ := agentdrelease.GenerateKeyPair()
	_, wrongPriv, _ := agentdrelease.GenerateKeyPair()
	pubPEM, _ := agentdrelease.FormatPublicKeyPEM(pub)

	distDir := t.TempDir()
	t.Setenv(agentdDistDirEnv, distDir)
	t.Setenv(agentdReleaseURLEnv, "off")
	t.Setenv(agentdReleasePublicKeyEnv, string(pubPEM))

	binPayload := []byte("agentd-bytes")
	m, _ := agentdrelease.NewManifestForBinary("v3.5.0", "linux/amd64", binPayload, nil)
	badSigned, _ := agentdrelease.SignManifest(m, wrongPriv, nil)
	raw, _ := json.Marshal(badSigned)
	_ = os.WriteFile(filepath.Join(distDir, "grain-agentd-linux-amd64"), binPayload, 0o755)
	_ = os.WriteFile(filepath.Join(distDir, "grain-agentd-linux-amd64.manifest.json"), raw, 0o644)

	srv := NewServer(Config{}, nil)

	req := httptest.NewRequest(http.MethodGet, "https://grain.example.com/api/hostd/agentd/linux/amd64/manifest.json", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("manifest signed by unpinned key returned %d, want 404", rec.Code)
	}
}

func TestControllerRefusesUnsignedAgentdFromReleaseURL(t *testing.T) {
	pub, _, _ := agentdrelease.GenerateKeyPair()
	pubPEM, _ := agentdrelease.FormatPublicKeyPEM(pub)

	// The hash matches, but nothing signed it: whoever serves the release
	// URL wrote both, so it proves nothing.
	binPayload := []byte("upstream-unsigned-agentd-binary")
	const rel = "v9.4.3"
	m, _ := agentdrelease.NewManifestForBinary(rel, "linux/amd64", binPayload, nil)
	rawManifest, _ := json.Marshal(m)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/" + rel + "/grain-agentd-linux-amd64.manifest.json":
			_, _ = w.Write(rawManifest)
		case "/releases/" + rel + "/grain-agentd-linux-amd64":
			_, _ = w.Write(binPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	t.Setenv(agentdDistDirEnv, t.TempDir()) // empty local dir
	t.Setenv(agentdReleaseURLEnv, upstream.URL+"/releases")
	t.Setenv(agentdReleasePublicKeyEnv, string(pubPEM))
	oldRel := cliInstallRelease
	cliInstallRelease = func() string { return rel }
	t.Cleanup(func() { cliInstallRelease = oldRel })

	srv := NewServer(Config{}, nil)
	for _, p := range []string{"/api/hostd/agentd/linux/amd64", "/api/hostd/agentd/linux/amd64/manifest.json"} {
		req := httptest.NewRequest(http.MethodGet, "https://grain.example.com"+p, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with an unsigned upstream release = %d, want 404", p, rec.Code)
		}
	}
}
