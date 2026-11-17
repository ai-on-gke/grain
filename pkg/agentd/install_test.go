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

package agentd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// fakeHome is a home directory with agy on $PATH, a token in the
// environment, and systemctl and loginctl recorded rather than run.
type fakeHome struct {
	home, agy string
	calls     *[]string
}

func newFakeHome(t *testing.T) fakeHome {
	t.Helper()
	notRoot(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	agy := filepath.Join(bin, "agy")
	writeFile(t, agy, "#!/bin/sh\n", 0o755)
	t.Setenv("PATH", bin)
	t.Setenv(TokenEnv, "ghd_install")
	t.Setenv("USER", "someone")

	var calls []string
	oldS, oldL := systemctl, loginctl
	systemctl = func(_ io.Writer, args ...string) error {
		calls = append(calls, "systemctl --user "+strings.Join(args, " "))
		return nil
	}
	loginctl = func(args ...string) ([]byte, error) {
		calls = append(calls, "loginctl "+strings.Join(args, " "))
		if args[0] == "show-user" {
			return []byte("Linger=no\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { systemctl, loginctl = oldS, oldL })
	return fakeHome{home: home, agy: agy, calls: &calls}
}

func TestInstall(t *testing.T) {
	h := newFakeHome(t)
	p := pathsFor(h.home)
	var out, errOut bytes.Buffer
	if code := install([]string{"-server", "https://grain.example.com", "-name", "ws-1"}, &out, &errOut); code != 0 {
		t.Fatalf("install = %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}

	// The binary.
	if st, err := os.Stat(p.Bin); err != nil || st.Mode()&0o111 == 0 {
		t.Errorf("the binary at %s: %v", p.Bin, err)
	}
	// The pinned release public key (~/.config/grain/agentd-release.pub), mode 0644.
	wantPub, err := release.EmbeddedPublicKey()
	if err != nil {
		t.Fatalf("EmbeddedPublicKey: %v", err)
	}
	gotPub, err := PinnedReleasePublicKey(h.home)
	if err != nil {
		t.Fatalf("PinnedReleasePublicKey: %v", err)
	}
	if !bytes.Equal(gotPub, wantPub) {
		t.Errorf("pinned release key %x != embedded %x", gotPub, wantPub)
	}
	if st, _ := os.Stat(p.ReleasePub); st == nil || st.Mode().Perm() != 0o644 {
		t.Errorf("release key file mode = %v, want 0644", st)
	}
	// The token, 0600, in a 0700 directory.
	if got, err := os.ReadFile(p.Token); err != nil || strings.TrimSpace(string(got)) != "ghd_install" {
		t.Errorf("token file = %q, %v", got, err)
	}
	if st, _ := os.Stat(p.Token); st == nil || st.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(p.Token)); st == nil || st.Mode().Perm() != 0o700 {
		t.Errorf("token directory mode = %v, want 0700", st.Mode().Perm())
	}
	// The allowlist: agy, as found on $PATH, shown on stdout before writing,
	// with env: [HOME, "*"] and ~/.grain/hostd-stage.
	if !strings.Contains(out.String(), "==> writing "+p.Allowlist+":") || !strings.Contains(out.String(), "path: "+h.agy) {
		t.Errorf("install stdout did not show the allowlist it writes:\n%s", out.String())
	}
	allow, _ := os.ReadFile(p.Allowlist)
	if !strings.Contains(string(allow), "path: "+h.agy) ||
		!strings.Contains(string(allow), "paths_under: [~/.grain/hostd-stage]") ||
		!strings.Contains(string(allow), `env: [HOME, "*"]`) ||
		!strings.Contains(string(allow), "capabilities: {}") {
		t.Errorf("allowlist:\n%s", allow)
	}
	if err := checkAgentsOnly(p.Allowlist, owners()); err != nil {
		t.Errorf("the installed allowlist: %v", err)
	}
	// The unit.
	unit, err := os.ReadFile(filepath.Join(p.UnitDir, UnitName))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ExecStart=" + p.Bin + " -server https://grain.example.com -token-file " + p.Token + " -allowlist " + p.Allowlist + " -state-dir " + p.StateDir + " -name ws-1"; !strings.Contains(string(unit), want) {
		t.Errorf("unit lacks %q:\n%s", want, unit)
	}
	// A drain or cutover exits 0 for the new binary to take over, so the
	// unit must restart on any exit (the 2026-11-11 §9 walkthrough found a
	// drained host left offline under Restart=on-failure).
	if !strings.Contains(string(unit), "\nRestart=always\n") || strings.Contains(string(unit), "Restart=on-failure") {
		t.Errorf("unit does not restart a drained grain-agentd (want Restart=always):\n%s", unit)
	}
	if !strings.Contains(string(unit), "\nKillMode=mixed\n") {
		t.Errorf("unit lacks KillMode=mixed:\n%s", unit)
	}
	// Started, and lingering enabled.
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable " + UnitName,
		"systemctl --user restart " + UnitName,
		"loginctl show-user someone -p Linger",
		"loginctl enable-linger someone",
	}
	if strings.Join(*h.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(*h.calls, "\n"), strings.Join(want, "\n"))
	}

	// Again, with no token in the environment: everything kept.
	t.Setenv(TokenEnv, "")
	writeFile(t, p.Allowlist, string(allow)+"# my edit\n", 0o644)
	out.Reset()
	if code := install([]string{"-server", "https://grain.example.com", "-no-start"}, &out, &errOut); code != 0 {
		t.Fatalf("install again = %d: %s", code, errOut.String())
	}
	for _, want := range []string{"keeping " + p.ReleasePub + " as it is", "keeping the token in " + p.Token, "keeping " + p.Allowlist + " as it is", "-no-start"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("second install's output lacks %q:\n%s", want, out.String())
		}
	}
	if again, _ := os.ReadFile(p.Allowlist); !strings.Contains(string(again), "# my edit") {
		t.Errorf("install overwrote an existing allowlist:\n%s", again)
	}
}

func TestInstallRefusesAnAllowlistWithCapabilities(t *testing.T) {
	h := newFakeHome(t)
	p := pathsFor(h.home)
	writeFile(t, p.Allowlist, "clis:\n  agy:\n    path: "+h.agy+"\ncapabilities:\n  x:\n    command: /bin/true\n", 0o644)
	var out, errOut bytes.Buffer
	if code := install([]string{"-server", "https://grain.example.com", "-no-start"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "runs agent CLIs only") {
		t.Fatalf("install = %d, %q; want 1 and the agents-only refusal", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(p.UnitDir, UnitName)); err == nil {
		t.Error("install wrote a unit for an allowlist it refused")
	}
}

func TestInstallWithNoAgentCLI(t *testing.T) {
	newFakeHome(t)
	t.Setenv("PATH", t.TempDir())
	var out, errOut bytes.Buffer
	if code := install([]string{"-server", "https://grain.example.com", "-no-start"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no agent CLI found") {
		t.Fatalf("install = %d, %q; want 1 and no agent CLI found", code, errOut.String())
	}
}

func TestInstallRefusesBadFlags(t *testing.T) {
	newFakeHome(t)
	for _, args := range [][]string{
		{},
		{"-server", "grain.example.com"},
		{"-server", "https://grain.example.com", "-name", "has space"},
		{"-server", "https://grain.example.com", "-iap-audience", "a$b"},
	} {
		var out, errOut bytes.Buffer
		if code := install(args, &out, &errOut); code != 2 {
			t.Errorf("install %q = %d, want 2 (%s)", args, code, errOut.String())
		}
	}
}

func TestInstallJoinHandshake(t *testing.T) {
	h := newFakeHome(t)
	t.Setenv(TokenEnv, "")
	p := pathsFor(h.home)

	var submitted hostd.JoinSubmitRequest
	var replay hostd.NonceReplayCache
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/hostd/join":
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			pub, err := hostd.VerifyJoinSubmit(submitted, time.Now().UTC(), &replay)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			nonce := "0123456789abcdef0123456789abcdef"
			code := hostd.DerivePairingCode("hjr_agentd1", nonce, submitted.PublicKey, submitted.Hostname)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(hostd.JoinSubmitResponse{
				ID:             "hjr_agentd1",
				ServerNonce:    nonce,
				PairingCode:    code,
				KeyFingerprint: hostd.KeyFingerprint(pub),
				EnrolledBy:     "someone@example.com",
				ExpiresAt:      time.Now().UTC().Add(24 * time.Hour),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/hostd/join/hjr_agentd1/poll":
			var poll hostd.JoinPollRequest
			_ = json.NewDecoder(r.Body).Decode(&poll)
			if err := hostd.VerifyJoinPoll("hjr_agentd1", submitted.PublicKey, poll, time.Now().UTC(), &replay); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(hostd.JoinPollResponse{
				Status:    "accepted",
				Name:      "agentd-laptop",
				Token:     "ghd_agentd_keybound_token",
				ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	if code := install([]string{"-server", ts.URL, "-join", "ghe_sharedcode123"}, &out, &errOut); code != 0 {
		t.Fatalf("install -join = %d: %s", code, errOut.String())
	}
	if submitted.EnrollmentCode != "ghe_sharedcode123" || len(submitted.CLIs) == 0 || submitted.CLIs[0] != "agy" {
		t.Fatalf("submitted = %+v, want enrollment code and agy CLI", submitted)
	}
	keyInfo, err := os.Stat(p.Key)
	if err != nil || keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("key file %s stat = %v, %v; want mode 0600", p.Key, keyInfo, err)
	}
	tokBytes, err := os.ReadFile(p.Token)
	if err != nil || strings.TrimSpace(string(tokBytes)) != "ghd_agentd_keybound_token" {
		t.Fatalf("token file = %q, %v", tokBytes, err)
	}
	unit, err := os.ReadFile(filepath.Join(p.UnitDir, UnitName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "-key-file "+p.Key) {
		t.Fatalf("unit missing -key-file %s:\n%s", p.Key, unit)
	}

	// Re-running `grain-agentd install -server <url>` (without -join) to
	// upgrade an older Restart=on-failure unit rewrites the unit with
	// Restart=always while preserving -key-file for the existing key-bound
	// credential.
	oldUnit := strings.ReplaceAll(string(unit), "Restart=always", "Restart=on-failure")
	writeFile(t, filepath.Join(p.UnitDir, UnitName), oldUnit, 0o644)
	out.Reset()
	errOut.Reset()
	if code := install([]string{"-server", ts.URL}, &out, &errOut); code != 0 {
		t.Fatalf("re-run install = %d: %s", code, errOut.String())
	}
	rewritten, err := os.ReadFile(filepath.Join(p.UnitDir, UnitName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rewritten), "\nRestart=always\n") || !strings.Contains(string(rewritten), "-key-file "+p.Key) {
		t.Fatalf("rewritten unit dropped Restart=always or -key-file %s:\n%s", p.Key, rewritten)
	}
}

func TestAllowlistFromTemplateGatesEnvOpenOnHostFeature(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stage := filepath.Join(home, ".grain", "hostd-stage")
	agy := filepath.Join(home, ".local", "bin", "agy")
	writeFile(t, agy, "#!/bin/sh\n", 0o755)
	t.Setenv("PATH", t.TempDir())

	tmpl := hostd.AllowlistTemplate{
		Version: hostd.AllowlistTemplateVersion,
		CLIs: []hostd.CLITemplateEntry{{
			Name:       "agy",
			Candidates: []string{"agy", "~/.local/bin/agy"},
			Env:        []string{"HOME", "AGY_CLI_DISABLE_AUTO_UPDATE", "GEMINI_API_KEY", "GOOGLE_API_KEY"},
			PassEnv:    hostd.ProxyVars,
		}},
	}

	modern, err := AllowlistFromTemplate(tmpl, hostd.ThisHello("v3.5.0"), home, stage, "https://grain.example.com")
	if err != nil {
		t.Fatalf("AllowlistFromTemplate(modern): %v", err)
	}
	if !strings.Contains(modern, "path: ~/.local/bin/agy\n") ||
		!strings.Contains(modern, "paths_under: [~/.grain/hostd-stage]\n") ||
		!strings.Contains(modern, `env: [HOME, "*"]`+"\n") {
		t.Errorf("AllowlistFromTemplate modern host:\n%s", modern)
	}

	legacy, err := AllowlistFromTemplate(tmpl, hostd.Hello{Protocol: 4}, home, stage, "https://grain.example.com")
	if err != nil {
		t.Fatalf("AllowlistFromTemplate(legacy): %v", err)
	}
	if strings.Contains(legacy, `"*"`) || strings.Contains(legacy, "~/") ||
		!strings.Contains(legacy, "path: "+agy+"\n") ||
		!strings.Contains(legacy, "env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]\n") {
		t.Errorf("AllowlistFromTemplate legacy host:\n%s", legacy)
	}
}

func TestInstallFetchesControllerTemplateAndResolvesHostCLIs(t *testing.T) {
	h := newFakeHome(t)
	// Remove agy and put claude + a custom controller-defined CLI on this host instead.
	binDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	optAider := filepath.Join(t.TempDir(), "opt", "aider")
	writeFile(t, claudeBin, "#!/bin/sh\n", 0o755)
	writeFile(t, optAider, "#!/bin/sh\n", 0o755)
	t.Setenv("PATH", binDir)
	p := pathsFor(h.home)

	controllerTmpl := hostd.AllowlistTemplate{
		Version: hostd.AllowlistTemplateVersion,
		CLIs: []hostd.CLITemplateEntry{
			{
				Name:       "agy",
				Candidates: []string{"agy", "~/.local/bin/agy"},
				Env:        []string{"HOME", "GEMINI_API_KEY"},
			},
			{
				Name:       "claude",
				Candidates: []string{"claude", "~/.local/bin/claude"},
				Env:        []string{"CLAUDE_CODE_OAUTH_TOKEN"},
				PassEnv:    []string{"HTTPS_PROXY"},
			},
			{
				Name:       "aider",
				Candidates: []string{optAider, "aider"},
				Env:        []string{"OPENAI_API_KEY"},
			},
		},
	}

	var templateFetches int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == hostd.InstallTemplateEndpoint && r.URL.Query().Get(hostd.InstallTemplateQuery) == hostd.InstallTemplateAgentd {
			templateFetches++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(controllerTmpl)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	if code := install([]string{"-server", ts.URL, "-name", "ws-tpl", "-no-start"}, &out, &errOut); code != 0 {
		t.Fatalf("install = %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if templateFetches != 1 {
		t.Errorf("template fetches = %d, want 1", templateFetches)
	}
	if !strings.Contains(out.String(), "claude:") || !strings.Contains(out.String(), "aider:") {
		t.Errorf("stdout did not show the entries being written:\n%s", out.String())
	}
	written, err := os.ReadFile(p.Allowlist)
	if err != nil {
		t.Fatal(err)
	}
	a, err := hostd.LoadAllowlist(p.Allowlist, owners())
	if err != nil {
		t.Fatalf("LoadAllowlist: %v\n%s", err, written)
	}
	if _, hasAgy := a.CLIs["agy"]; hasAgy {
		t.Errorf("allowlist unexpectedly included missing agy:\n%s", written)
	}
	if e := a.CLIs["claude"]; e == nil || e.Path != claudeBin {
		t.Errorf("claude entry = %+v, want %s", e, claudeBin)
	}
	if e := a.CLIs["aider"]; e == nil || e.Path != optAider {
		t.Errorf("aider entry = %+v, want %s", e, optAider)
	}
}

func TestAllowCommandConfirmsAndAddsControllerEntry(t *testing.T) {
	h := newFakeHome(t)
	p := pathsFor(h.home)
	newCLI := filepath.Join(h.home, ".local", "bin", "new-cli")
	writeFile(t, newCLI, "#!/bin/sh\n", 0o755)

	controllerTmpl := hostd.AllowlistTemplate{
		Version: hostd.AllowlistTemplateVersion,
		CLIs: []hostd.CLITemplateEntry{
			{
				Name:       "agy",
				Candidates: []string{"agy", "~/.local/bin/agy"},
				Env:        []string{"HOME", "GEMINI_API_KEY"},
			},
			{
				Name:       "new-cli",
				Aliases:    []string{"newcli"},
				Candidates: []string{"new-cli", "~/.local/bin/new-cli"},
				Env:        []string{"NEW_CLI_API_KEY", "NEW_CLI_MODEL"},
				PassEnv:    []string{"HTTPS_PROXY"},
			},
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == hostd.InstallTemplateEndpoint && r.URL.Query().Get(hostd.InstallTemplateQuery) == hostd.InstallTemplateAgentd {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(controllerTmpl)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	// Install first with only agy on $PATH (before new-cli is selected); remove new-cli during install.
	if err := os.Remove(newCLI); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := install([]string{"-server", ts.URL, "-name", "ws-allow", "-no-start"}, &out, &errOut); code != 0 {
		t.Fatalf("install = %d: %s", code, errOut.String())
	}

	// Now install new-cli on the host and run `grain-agentd allow` (reading -server from the installed unit).
	writeFile(t, newCLI, "#!/bin/sh\n", 0o755)

	// 1. Non-interactive EOF without -yes refuses to write.
	out.Reset()
	errOut.Reset()
	if code := allow([]string{"new-cli"}, strings.NewReader(""), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "confirmation required") {
		t.Fatalf("allow with EOF = %d, %q; want non-zero and confirmation required", code, errOut.String())
	}

	// 2. Answering "n" cancels without touching agentd.yaml.
	out.Reset()
	errOut.Reset()
	if code := allow([]string{"new-cli"}, strings.NewReader("n\n"), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "cancelled") {
		t.Fatalf("allow with 'n' = %d, %q; want non-zero and cancelled", code, errOut.String())
	}
	if a, _ := hostd.LoadAllowlist(p.Allowlist, owners()); a.CLIs["new-cli"] != nil {
		t.Fatal("cancelled allow modified the allowlist")
	}

	// 3. Answering "y" (even via alias "newcli") shows the snippet and writes new-cli while keeping agy.
	out.Reset()
	errOut.Reset()
	if code := allow([]string{"newcli"}, strings.NewReader("y\n"), &out, &errOut); code != 0 {
		t.Fatalf("allow with 'y' = %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "new-cli:") || !strings.Contains(out.String(), "path: ~/.local/bin/new-cli") {
		t.Errorf("allow stdout missing entry preview:\n%s", out.String())
	}
	a, err := hostd.LoadAllowlist(p.Allowlist, owners())
	if err != nil {
		t.Fatalf("LoadAllowlist after allow: %v", err)
	}
	if a.CLIs["agy"] == nil || a.CLIs["new-cli"] == nil || a.CLIs["new-cli"].Path != newCLI {
		t.Fatalf("CLIs after allow = %+v, want both agy and new-cli (%s)", a.CLIs, newCLI)
	}

	// 4. Running again when already allowed exits 0 without prompting.
	out.Reset()
	errOut.Reset()
	if code := allow([]string{"new-cli"}, strings.NewReader(""), &out, &errOut); code != 0 || !strings.Contains(out.String(), "already allows new-cli") {
		t.Fatalf("repeat allow = %d, out=%q, err=%q", code, out.String(), errOut.String())
	}
}

func TestAllowRespectsOlderTargetAgentdWithoutEnvOpen(t *testing.T) {
	h := newFakeHome(t)
	allowlistPath := filepath.Join(t.TempDir(), "agentd.yaml")
	claudeBin := filepath.Join(h.home, ".local", "bin", "claude")
	writeFile(t, claudeBin, "#!/bin/sh\n", 0o755)

	// Fake older grain-agentd binary that rejects `env: ["*"]` on -check.
	legacyBin := filepath.Join(t.TempDir(), "legacy-grain-agentd")
	writeFile(t, legacyBin, "#!/bin/sh\nwhile IFS= read -r line; do\n  case \"$line\" in\n    *'\"*\"'*) exit 2 ;;\n  esac\ndone < \"$3\"\nexit 0\n", 0o755)

	controllerTmpl := hostd.AllowlistTemplate{
		Version: hostd.AllowlistTemplateVersion,
		CLIs: []hostd.CLITemplateEntry{{
			Name:       "claude",
			Candidates: []string{"~/.local/bin/claude"},
			Env:        []string{"CLAUDE_CODE_OAUTH_TOKEN", "MCP_TOOL_TIMEOUT"},
			PassEnv:    []string{"HTTPS_PROXY"},
		}},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controllerTmpl)
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	args := []string{
		"claude",
		"-server", ts.URL,
		"-allowlist", allowlistPath,
		"-target-bin", legacyBin,
		"-yes",
	}
	if code := allow(args, nil, &out, &errOut); code != 0 {
		t.Fatalf("allow targeting legacy binary = %d: %s", code, errOut.String())
	}
	raw, err := os.ReadFile(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, `"*"`) || strings.Contains(text, "~/") {
		t.Fatalf("allow emitted FeatureEnvOpen syntax for legacy target binary:\n%s", text)
	}
	if !strings.Contains(text, "path: "+claudeBin+"\n") ||
		!strings.Contains(text, "env: [CLAUDE_CODE_OAUTH_TOKEN, MCP_TOOL_TIMEOUT]\n") {
		t.Fatalf("allow missing closed env or absolute path for legacy binary:\n%s", text)
	}
}
