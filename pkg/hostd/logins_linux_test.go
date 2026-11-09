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

package hostd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoginsUnderReadsEachCLIsOwnLogin(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	noEnv := func(string) string { return "" }

	home := t.TempDir()
	if got := loginsUnder(home, noEnv); len(got) != 0 {
		t.Errorf("empty home: logins = %q, want none", got)
	}
	write(t, filepath.Join(home, ".codex", "auth.json"))
	write(t, filepath.Join(home, ".claude", ".credentials.json"))
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "antigravity-cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []string{LoginAntigravity, LoginClaude, LoginCodex}
	if got := loginsUnder(home, noEnv); !reflect.DeepEqual(got, want) {
		t.Errorf("logins = %q, want %q", got, want)
	}

	// $CODEX_HOME and $CLAUDE_CONFIG_DIR move the login, as they move
	// the CLI's own reading of it.
	other := t.TempDir()
	env := func(k string) string {
		switch k {
		case "CODEX_HOME":
			return filepath.Join(other, "codex")
		case "CLAUDE_CONFIG_DIR":
			return filepath.Join(other, "claude")
		}
		return ""
	}
	if got := loginsUnder(home, env); !reflect.DeepEqual(got, []string{LoginAntigravity}) {
		t.Errorf("with CODEX_HOME and CLAUDE_CONFIG_DIR elsewhere: logins = %q, want antigravity alone", got)
	}
	write(t, filepath.Join(other, "codex", "auth.json"))
	if got := loginsUnder(home, env); !reflect.DeepEqual(got, []string{LoginAntigravity, LoginCodex}) {
		t.Errorf("with $CODEX_HOME/auth.json: logins = %q", got)
	}
	// A directory where the file should be is not a login.
	gem := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gem, ".codex", "auth.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := loginsUnder(gem, noEnv); len(got) != 0 {
		t.Errorf("auth.json a directory: logins = %q, want none", got)
	}
}

// A hostd that dials out reports its machine's agent CLI logins in its
// hello, and the controller keeps them in that hostd's status for the
// host pickers in Settings.
func TestAHostdsLoginsReachTheControllersStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-home"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	if err := os.MkdirAll(filepath.Join(home, "codex-home"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "codex-home", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := startController(t, "s3cret")
	startRemoteServer(t, ctl, "s3cret", "")

	deadline := time.Now().Add(10 * time.Second)
	for {
		st := ctl.remote.Status()
		if st.LoginsKnown {
			if !reflect.DeepEqual(st.Logins, []string{LoginCodex}) {
				t.Fatalf("status logins = %q, want codex alone", st.Logins)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the controller never learned the hostd's logins: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
