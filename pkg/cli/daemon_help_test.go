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
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// TestDaemonHelpUnchanged pins the whole of `grain daemon -help`: every
// flag's name, default and help text, in the order flag prints them.
// The daemon's wiring moved out of this package into pkg/daemon
// (grain/task-2100) with this package keeping only the flags, and this is
// what says the move changed none of them. A change that means to
// change a flag regenerates the golden file:
//
//	go run ./cmd/grain daemon -help 2> cmd/grain/testdata/daemon_help.golden
func TestDaemonHelpUnchanged(t *testing.T) {
	// The help text is the same whatever the environment holds today, but
	// a default read from the environment would make it this host's.
	for _, env := range []string{dataDirEnvVar, "GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}
	_, fs, _, err := parseDaemonArgs([]string{"-help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseDaemonArgs(-help) = %v, want flag.ErrHelp", err)
	}
	var got bytes.Buffer
	writeDaemonUsage(fs, &got)
	want, err := os.ReadFile(filepath.Join("testdata", "daemon_help.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		gl, wl := bytes.Split(got.Bytes(), []byte("\n")), bytes.Split(want, []byte("\n"))
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w []byte
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if !bytes.Equal(g, w) {
				t.Fatalf("grain daemon -help differs from testdata/daemon_help.golden at line %d:\n got: %s\nwant: %s", i+1, g, w)
			}
		}
	}
}
