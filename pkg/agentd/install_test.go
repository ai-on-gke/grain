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
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// The allowlist: agy, as found on $PATH, and nothing else.
	allow, _ := os.ReadFile(p.Allowlist)
	if !strings.Contains(string(allow), "path: "+h.agy) || !strings.Contains(string(allow), "capabilities: {}") {
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
	for _, want := range []string{"keeping the token in " + p.Token, "keeping " + p.Allowlist + " as it is", "-no-start"} {
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
