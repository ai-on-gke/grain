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

package hostinit

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Under auto overcommit the kernel's OOM killer has to pick a sandbox,
// never the daemon or hostd: their units run strongly negative, and the
// sandboxes run positive (docs/tuning-stats.md, auto overcommit).
func TestDaemonAndHostdUnitsAreProtectedFromTheOOMKiller(t *testing.T) {
	if unit := renderUnit("", "/usr/local/bin/grain daemon"); !strings.Contains(unit, "\nOOMScoreAdjust=-900\n") {
		t.Errorf("host-mode grain-daemon.service carries no OOMScoreAdjust=-900:\n%s", unit)
	}
	setup, err := os.ReadFile("../../scripts/setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(setup)
	if !regexp.MustCompile(`DOCKER_ARGS=\([^)]*--oom-score-adj -900`).MatchString(text) {
		t.Error("setup.sh's grain-daemon container is not started with --oom-score-adj -900")
	}
	hostd := regexp.MustCompile(`(?s)cat > "\$HOSTD_UNIT" <<UNIT(.*?)\nUNIT`).FindStringSubmatch(text)
	if hostd == nil || !strings.Contains(hostd[1], "\nOOMScoreAdjust=-900\n") {
		t.Error("setup.sh's grain-hostd.service carries no OOMScoreAdjust=-900")
	}
}
