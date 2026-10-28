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

package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// TestStopTimeoutsCoverTheShutdownDrain holds every supervisor that
// stops the daemon to the drain the daemon itself does (shutdownDrain's
// own doc comment): a `docker stop --time`, a TimeoutStopSec or a
// terminationGracePeriodSeconds below shutdownStopGrace kills the daemon
// part way through releasing its runs' sandboxes, which is the
// disagreement this test exists to keep from coming back.
func TestStopTimeoutsCoverTheShutdownDrain(t *testing.T) {
	t.Parallel()
	if shutdownStopGrace <= shutdownDrain {
		t.Fatalf("shutdownStopGrace = %s, want more than shutdownDrain (%s)", shutdownStopGrace, shutdownDrain)
	}

	root := filepath.Join("..", "..")
	cases := []struct {
		file    string
		pattern string
		// want is how many matches the file must have, so that a value
		// renamed out from under its pattern fails rather than passes
		// with nothing checked.
		want int
	}{
		{"scripts/setup.sh", `stop --time (\d+) grain-daemon`, 1},
		// The one daemon unit setup.sh writes: the container unit (host
		// mode's was removed with it, task 5041).
		{"scripts/setup.sh", `(?m)^TimeoutStopSec=(\d+)$`, 1},
		{"deploy/kubernetes/grain.yaml", `terminationGracePeriodSeconds: (\d+)`, 1},
		{"deploy/kubernetes/grain-granule.yaml", `terminationGracePeriodSeconds: (\d+)`, 1},
	}
	dockerStop := time.Duration(0)
	for _, c := range cases {
		data, err := os.ReadFile(filepath.Join(root, c.file))
		if err != nil {
			t.Fatal(err)
		}
		matches := regexp.MustCompile(c.pattern).FindAllStringSubmatch(string(data), -1)
		if len(matches) != c.want {
			t.Errorf("%s: %d match(es) for %q, want %d", c.file, len(matches), c.pattern, c.want)
			continue
		}
		for _, m := range matches {
			secs, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("%s: %q: %v", c.file, m[0], err)
			}
			got := time.Duration(secs) * time.Second
			if got < shutdownStopGrace {
				t.Errorf("%s: %q allows %s, want at least %s (shutdownDrain %s plus slack)",
					c.file, m[0], got, shutdownStopGrace, shutdownDrain)
			}
			if c.pattern == cases[0].pattern {
				dockerStop = got
			}
		}
	}
	// The container unit's TimeoutStopSec is the outer bound on its own
	// ExecStop: systemd SIGKILLs the docker client, and with it the stop,
	// once it runs out. So it must exceed the docker stop it covers.
	data, err := os.ReadFile(filepath.Join(root, "scripts/setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^TimeoutStopSec=(\d+)$`).FindAllStringSubmatch(string(data), -1) {
		secs, _ := strconv.Atoi(m[1])
		if got := time.Duration(secs) * time.Second; dockerStop > 0 && got <= dockerStop {
			t.Errorf("scripts/setup.sh: %q allows %s, want more than the unit's docker stop (%s)", m[0], got, dockerStop)
		}
	}
}
