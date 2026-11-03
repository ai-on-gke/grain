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
	"flag"
	"io"
	"strings"
	"testing"
)

// grain create and grain update read -priority 1e3 as POST /api/tasks
// does, 1000, rather than refusing it as flag.Int did
// (docs/test-runs/2026-11-03-sandbox-cli-edges.md).
func TestPriorityFlagReadsTheAPIsSpellings(t *testing.T) {
	for spelling, want := range map[string]int{"8": 8, "1e3": 1000, "1000.0": 1000, "1.5e2": 150, "-1": -1} {
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		p := priorityFlag(fs, "")
		if err := fs.Parse([]string{"-priority", spelling}); err != nil || int(*p) != want {
			t.Errorf("-priority %s = %d, %v; want %d", spelling, *p, err, want)
		}
	}
	for _, bad := range []string{"1.5", "1e20", "x"} {
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		priorityFlag(fs, "")
		if err := fs.Parse([]string{"-priority", bad}); err == nil || !strings.Contains(err.Error(), "not a whole number") {
			t.Errorf("-priority %s: %v, want refused as not a whole number", bad, err)
		}
	}
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	if p := priorityFlag(fs, ""); *p != 5 {
		t.Errorf("default -priority = %d, want 5", *p)
	}
}
