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
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
		text := string(data)
		if c.file == "scripts/setup.sh" {
			text = daemonUnitText(t, text)
		}
		matches := regexp.MustCompile(c.pattern).FindAllStringSubmatch(text, -1)
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
	for _, m := range regexp.MustCompile(`(?m)^TimeoutStopSec=(\d+)$`).FindAllStringSubmatch(daemonUnitText(t, string(data)), -1) {
		secs, _ := strconv.Atoi(m[1])
		if got := time.Duration(secs) * time.Second; dockerStop > 0 && got <= dockerStop {
			t.Errorf("scripts/setup.sh: %q allows %s, want more than the unit's docker stop (%s)", m[0], got, dockerStop)
		}
	}
}

// daemonUnitText is the grain-daemon.service heredoc in setup.sh: the
// other units it writes (grain-postgres.service among them) have stop
// timeouts of their own, for other processes.
func daemonUnitText(t *testing.T, setup string) string {
	t.Helper()
	const open = "cat > /etc/systemd/system/grain-daemon.service <<UNIT\n"
	i := strings.Index(setup, open)
	if i < 0 {
		t.Fatalf("scripts/setup.sh: no %q", strings.TrimSpace(open))
	}
	unit := setup[i+len(open):]
	j := strings.Index(unit, "\nUNIT\n")
	if j < 0 {
		t.Fatalf("scripts/setup.sh: grain-daemon.service's heredoc never closes")
	}
	return unit[:j+1]
}

func TestRunHostUpgradeResumeIsNoOpOnceStopping(t *testing.T) {
	t.Parallel()
	stopping := make(chan struct{})
	u := newRunHostUpgrade(nil, nil, true).withStopping(stopping)
	readopted := 0
	u.setReadopt(func() { readopted++ })

	close(stopping)
	u.resume()
	if readopted != 0 {
		t.Fatalf("readopt called %d time(s) after stopping closed, want 0", readopted)
	}
}

func TestStopWithGraceBoundsListenerShutdown(t *testing.T) {
	t.Parallel()
	var hasDeadline bool
	var remaining time.Duration
	stopWithGrace(func(ctx context.Context) error {
		var deadline time.Time
		deadline, hasDeadline = ctx.Deadline()
		remaining = time.Until(deadline)
		return nil
	})
	if !hasDeadline {
		t.Fatal("stopWithGrace passed a context without a deadline")
	}
	if remaining <= 0 || remaining > shutdownServerGrace {
		t.Fatalf("stopWithGrace deadline remaining = %s, want in (0, %s]", remaining, shutdownServerGrace)
	}
}
