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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maxCmdFileLines is the most lines a non-test Go file in cmd/grain may
// have. cmd/grain is flag parsing, subcommand dispatch and printing; a
// file that grows past this is usually logic that belongs in a pkg/
// package, where something other than this binary can use it and test it
// without a CLI in front (grain/task-2101).
const maxCmdFileLines = 800

// licenseHeaderLines is the Apache header at the top of every file, and
// the blank line after it, which the limit does not count.
const licenseHeaderLines = 14

// oversizedCmdFiles are the files allowed past maxCmdFileLines, each with
// the reason it is still here and the most lines it may grow to. Lower a
// cap when a file shrinks; do not raise one to make room for logic.
var oversizedCmdFiles = map[string]struct {
	max    int
	reason string
}{
	// One flag.FlagSet declaration per daemon setting (over 150 of
	// them), each with its help text, parsed into a daemon.Config. The
	// daemon's wiring itself is pkg/daemon; what is left is flags.
	"daemon.go": {max: 1500, reason: "the daemon's flag set and its validation"},
}

// TestCmdGrainFilesStaySmall keeps cmd/grain a CLI rather than a second
// home for grain's logic.
func TestCmdGrainFilesStaySmall(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// The license header every file opens with is not logic.
		lines := bytes.Count(data, []byte("\n")) - licenseHeaderLines
		limit := maxCmdFileLines
		if allowed, ok := oversizedCmdFiles[name]; ok {
			seen[name] = true
			limit = allowed.max
		}
		if lines > limit {
			t.Errorf("pkg/cli/%s is %d lines, over its limit of %d: move the logic in it to a package of its own and leave the flags and printing here", name, lines, limit)
		}
	}
	for name, allowed := range oversizedCmdFiles {
		if !seen[name] {
			t.Errorf("oversizedCmdFiles names %s (%s), which no longer exists: drop it", name, allowed.reason)
		}
	}
}
