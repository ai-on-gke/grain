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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
}

// notRoot makes the process look unprivileged for the test, whatever it
// runs as.
func notRoot(t *testing.T) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = old })
}

func TestCheckAgentsOnly(t *testing.T) {
	dir := t.TempDir()
	cli := "clis:\n  agy:\n    path: /bin/true\n    paths_under: [" + dir + "]\n    cwd_under: [" + dir + "]\n"
	cases := []struct {
		name, content string
		perm          os.FileMode
		wantErr       string
	}{
		{"agent CLI only", cli + "capabilities: {}\n", 0o644, ""},
		{"capabilities refused", cli + "capabilities:\n  x:\n    command: /bin/true\n", 0o644, "names capabilities (x)"},
		{"no CLIs", "clis: {}\n", 0o644, "offers no agent CLIs"},
		{"group-writable refused", cli, 0o664, "writable by group or others"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "agentd.yaml")
			writeFile(t, p, c.content, c.perm)
			err := checkAgentsOnly(p, owners())
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("checkAgentsOnly: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("checkAgentsOnly = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		err := checkAgentsOnly(filepath.Join(t.TempDir(), "none.yaml"), owners())
		if err == nil || !strings.Contains(err.Error(), "no allowlist at") {
			t.Fatalf("checkAgentsOnly = %v, want a missing-allowlist error", err)
		}
	})
}

func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agentd-token")
	t.Setenv(TokenEnv, "ghd_env")
	if got, err := readToken(p); err != nil || got != "ghd_env" {
		t.Fatalf("readToken with no file = %q, %v; want the environment's", got, err)
	}
	writeFile(t, p, "  ghd_file\n", 0o600)
	if got, err := readToken(p); err != nil || got != "ghd_file" {
		t.Fatalf("readToken = %q, %v; want the file's", got, err)
	}
	t.Setenv(TokenEnv, "")
	if _, err := readToken(filepath.Join(dir, "none")); err == nil {
		t.Fatal("readToken with neither: want an error")
	}
}

func TestDefaultAllowlist(t *testing.T) {
	stage := t.TempDir()
	content := defaultAllowlist(stage, "https://grain.example.com", "/opt/agy/bin/agy")
	p := filepath.Join(t.TempDir(), "agentd.yaml")
	writeFile(t, p, content, 0o644)
	if err := checkAgentsOnly(p, owners()); err != nil {
		t.Fatalf("the default allowlist does not pass checkAgentsOnly: %v\n%s", err, content)
	}
	a, err := hostd.LoadAllowlist(p, owners())
	if err != nil {
		t.Fatal(err)
	}
	if e := a.CLIs["agy"]; e == nil || e.Path != "/opt/agy/bin/agy" {
		t.Fatalf("agy entry = %+v, want path /opt/agy/bin/agy", e)
	}
}

func TestFindAgy(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	none := func(string) bool { return false }
	if got := findAgy(home, none); got != "" {
		t.Fatalf("findAgy with nothing executable = %q, want none", got)
	}
	// On $PATH.
	writeFile(t, filepath.Join(bin, "agy"), "#!/bin/sh\n", 0o755)
	if got := findAgy(home, executable); got != filepath.Join(bin, "agy") {
		t.Fatalf("findAgy = %q, want the one on $PATH", got)
	}
	// A registered path is tried first.
	registered := filepath.Join(t.TempDir(), "cli")
	writeFile(t, registered, "#!/bin/sh\n", 0o755)
	old := agyPaths
	t.Cleanup(func() { agyPaths = old })
	RegisterAgyPath(registered)
	if got := findAgy(home, executable); got != registered {
		t.Fatalf("findAgy = %q, want the registered %s", got, registered)
	}
}

func TestRenderUnit(t *testing.T) {
	u := unitConfig{Bin: "/home/u/.local/bin/grain-agentd", Server: "https://grain.example.com", Name: "u-1",
		Token: "/home/u/.config/grain/agentd-token", Allowlist: "/home/u/.config/grain/agentd.yaml",
		StateDir: "/home/u/.local/state/grain-agentd", IAPAudience: "123.apps.example",
		Extra: []string{"-via", "/opt/helper"}}
	text, err := renderUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ExecStart=/home/u/.local/bin/grain-agentd -server https://grain.example.com -token-file /home/u/.config/grain/agentd-token",
		"-name u-1", "-iap-audience 123.apps.example", "-via /opt/helper", "WantedBy=default.target",
		"Description=grain-agentd: agent CLIs for the grain daemon at https://grain.example.com",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit lacks %q:\n%s", want, text)
		}
	}
	u.StateDir = "/home/u/my state"
	if _, err := renderUnit(u); err == nil {
		t.Error("renderUnit with a space in a path: want an error")
	}
	u.StateDir = "/home/u/state"
	u.Extra = []string{"-via", "100%"}
	if _, err := renderUnit(u); err == nil {
		t.Error("renderUnit with a %% in a transport's argument: want an error")
	}
}

func TestRefusesRoot(t *testing.T) {
	old := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = old })
	var out, errOut bytes.Buffer
	if code := run([]string{"-server", "https://grain.example.com"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "refusing to run as root") {
		t.Errorf("run as root = %d, %q; want 2 and a refusal", code, errOut.String())
	}
	errOut.Reset()
	if code := install([]string{"-server", "https://grain.example.com"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "not root") {
		t.Errorf("install as root = %d, %q; want 2 and a refusal", code, errOut.String())
	}
}

func TestRunCheckAndBuild(t *testing.T) {
	notRoot(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "agentd.yaml")
	writeFile(t, p, defaultAllowlist(dir, "https://grain.example.com", "/bin/true"), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"-allowlist", p, "-check"}, &out, &errOut); code != 0 {
		t.Fatalf("-check = %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "agy") || !strings.Contains(out.String(), "/bin/true") {
		t.Errorf("-check printed %q, want the agy entry", out.String())
	}

	writeFile(t, p, "clis:\n  agy:\n    path: /bin/true\ncapabilities:\n  x:\n    command: /bin/true\n", 0o644)
	out.Reset()
	errOut.Reset()
	if code := run([]string{"-allowlist", p, "-check"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "runs agent CLIs only") {
		t.Errorf("-check with capabilities = %d, %q; want 2 and the agents-only refusal", code, errOut.String())
	}

	out.Reset()
	// -build prints the release stamped in at link time (empty in a test
	// binary) and exits before anything else is checked.
	if code := run([]string{"-build", "-allowlist", filepath.Join(dir, "none.yaml")}, &out, &errOut); code != 0 {
		t.Errorf("-build = %d, %q", code, errOut.String())
	}
}

func TestRunNeedsServerAndToken(t *testing.T) {
	notRoot(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "agentd.yaml")
	writeFile(t, p, defaultAllowlist(dir, "https://grain.example.com", "/bin/true"), 0o644)
	t.Setenv(TokenEnv, "")
	var out, errOut bytes.Buffer
	if code := run([]string{"-allowlist", p}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "-server is required") {
		t.Errorf("no -server = %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"-allowlist", p, "-server", "https://grain.example.com", "-token-file", filepath.Join(dir, "none")}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "no host token") {
		t.Errorf("no token = %d, %q", code, errOut.String())
	}
	errOut.Reset()
	t.Setenv(TokenEnv, "ghd_x")
	if code := run([]string{"-allowlist", p, "-server", "grain.example.com"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "is not a URL") {
		t.Errorf("a bare host = %d, %q", code, errOut.String())
	}
}
