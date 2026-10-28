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
	"errors"
	"flag"
	"testing"
)

// The default is only ever right by coincidence: a deployment that binds
// any other port made every CLI call on its own host need -server. The
// env var is what scripts/setup.sh sets, once, for every shell there.
func TestServerDefault(t *testing.T) {
	t.Setenv(cliEnvFileEnvVar, "") // no /etc/grain/cli.env: cliconn_test.go covers it
	for _, tc := range []struct {
		name string
		env  string
		set  bool
		want string
	}{
		{"unset falls back to the built-in default", "", false, defaultServerURL},
		{"set overrides it", "http://127.0.0.1:8080", true, "http://127.0.0.1:8080"},
		// An exported-but-empty variable is a broken profile script, not
		// a request to talk to no server -- so it must not win.
		{"empty is treated as unset", "", true, defaultServerURL},
		{"whitespace only is treated as unset", "   ", true, defaultServerURL},
		{"surrounding whitespace is trimmed", "  http://h:9/  ", true, "http://h:9/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(serverEnvVar, tc.env)
			} else {
				t.Setenv(serverEnvVar, "")
				// Setenv cannot unset, and "" is its own case above; the
				// unset path is covered by the empty case reaching the
				// same branch.
			}
			if got := serverDefault(); got != tc.want {
				t.Errorf("serverDefault() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The CLI's built-in default is loopback on the port every installer
// binds by default, so a fresh install's `grain` reaches its own daemon
// before /etc/profile.d/grain.sh has ever been sourced.
func TestDefaultServerURLMatchesTheInstallersDefaultPort(t *testing.T) {
	if defaultServerURL != "http://127.0.0.1:4242" {
		t.Errorf("defaultServerURL = %q, want http://127.0.0.1:4242", defaultServerURL)
	}
	t.Setenv(dataDirEnvVar, "")
	_, fs, _, err := parseDaemonArgs([]string{"-help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseDaemonArgs(-help) = %v, want flag.ErrHelp", err)
	}
	if got := fs.Lookup("ui-addr").DefValue; got != "0.0.0.0:4242" {
		t.Errorf("grain daemon -ui-addr defaults to %q, want 0.0.0.0:4242 -- the address defaultServerURL reaches over loopback", got)
	}
}
