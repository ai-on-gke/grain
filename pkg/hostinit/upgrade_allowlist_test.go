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
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The unit grain init writes carries -upgrade-allowed-branches when it
// was given one, keeps the one the unit already on disk passes when it
// was not, and refuses a malformed one either way (director task 1913).
// Not t.Parallel(): every test here redirects systemdUnitPath.

// startFresh runs ensureDaemon on a host with no daemon running and
// returns its step and the unit it wrote (empty if it wrote none).
func startFresh(t *testing.T, opts Options) (Step, string) {
	t.Helper()
	unitPath := systemdUnitPath
	sys := newFakeSystem()
	sys.root = true
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")
	var r Result
	ensureDaemon(context.Background(), sys, opts, &r, "172.30.0.1")
	s, ok := stepStatus(r, "start the daemon")
	if !ok {
		t.Fatalf("no \"start the daemon\" step in %+v", r.Steps)
	}
	data, _ := os.ReadFile(unitPath)
	return s, string(data)
}

func installUnit(t *testing.T, path, execStart string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("[Service]\nExecStart="+execStart+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonUnitCarriesTheGivenUpgradeAllowlist(t *testing.T) {
	withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	opts.UpgradeAllowedBranches = []string{"main", "latest", "release/*"}
	s, unit := startFresh(t, opts)
	if s.Status != StepDone {
		t.Fatalf("got %+v, want StepDone", s)
	}
	if !strings.Contains(unit, " -upgrade-allowed-branches main,latest,release/*") {
		t.Errorf("unit does not carry the given allowlist:\n%s", unit)
	}

	// Given none, with nothing on disk: no flag, the daemon's default.
	withTempUnitPath(t)
	_, unit = startFresh(t, baseOptions(t).withDefaults())
	if strings.Contains(unit, "upgrade-allowed-branches") {
		t.Errorf("unit passes an allowlist nobody gave:\n%s", unit)
	}
}

func TestDaemonUnitKeepsTheInstalledUpgradeAllowlist(t *testing.T) {
	for name, flag := range map[string]string{
		"as written":   "-upgrade-allowed-branches main,candidate,latest",
		"with =":       "--upgrade-allowed-branches=main,candidate,latest",
		"last wins":    "-upgrade-allowed-branches main -upgrade-allowed-branches main,candidate,latest",
		"quoted value": `-upgrade-allowed-branches "main,candidate,latest"`,
	} {
		t.Run(name, func(t *testing.T) {
			unitPath := withTempUnitPath(t)
			opts := baseOptions(t).withDefaults()
			installUnit(t, unitPath, "/usr/local/bin/grain daemon -data-dir "+opts.DataDir+" "+flag)
			s, unit := startFresh(t, opts)
			if s.Status != StepDone {
				t.Fatalf("got %+v, want StepDone", s)
			}
			if !strings.Contains(unit, " -upgrade-allowed-branches main,candidate,latest") {
				t.Errorf("the rewritten unit dropped the installed allowlist:\n%s", unit)
			}
			if !strings.Contains(s.Detail, "main,candidate,latest") {
				t.Errorf("the step does not say which allowlist it kept: %q", s.Detail)
			}
		})
	}
}

func TestDaemonUnitOverridesTheInstalledUpgradeAllowlist(t *testing.T) {
	unitPath := withTempUnitPath(t)
	opts := baseOptions(t).withDefaults()
	installUnit(t, unitPath, "/usr/local/bin/grain daemon -upgrade-allowed-branches main,latest")
	opts.UpgradeAllowedBranches = []string{"**"}
	_, unit := startFresh(t, opts)
	if !strings.Contains(unit, " -upgrade-allowed-branches **") || strings.Contains(unit, "main,latest") {
		t.Errorf("the given allowlist did not replace the installed one:\n%s", unit)
	}

	// A -daemon-arg naming the flag is this run's own value too.
	unitPath = withTempUnitPath(t)
	installUnit(t, unitPath, "/usr/local/bin/grain daemon -upgrade-allowed-branches main,latest")
	opts = baseOptions(t).withDefaults()
	opts.ExtraDaemonArgs = []string{"-upgrade-allowed-branches", "main"}
	_, unit = startFresh(t, opts)
	if strings.Contains(unit, "main,latest") {
		t.Errorf("the installed allowlist was carried over beside -daemon-arg's own:\n%s", unit)
	}
}

func TestMalformedUpgradeAllowlistIsRefused(t *testing.T) {
	for _, bad := range [][]string{{"main", "release/["}, {"feature/[]x"}, {"main latest"}, {"$(id)"}} {
		opts := baseOptions(t)
		opts.UpgradeAllowedBranches = bad
		if err := opts.withDefaults().validate(); err == nil {
			t.Errorf("validate accepted the allowlist %q", bad)
		}
	}
	for _, good := range [][]string{{"main", "candidate", "latest", "v[0-9]*.[0-9]*.[0-9]*"}, {"**"}, {"release/*"}} {
		opts := baseOptions(t)
		opts.UpgradeAllowedBranches = good
		if err := opts.withDefaults().validate(); err != nil {
			t.Errorf("validate refused the allowlist %q: %v", good, err)
		}
	}

	// One already in the unit is not carried into the new one silently.
	unitPath := withTempUnitPath(t)
	installUnit(t, unitPath, "/usr/local/bin/grain daemon -upgrade-allowed-branches main,rel[ease")
	s, _ := startFresh(t, baseOptions(t).withDefaults())
	if s.Status != StepRefused || !strings.Contains(s.Detail, "-upgrade-allowed-branches") {
		t.Errorf("got %+v, want StepRefused naming -upgrade-allowed-branches", s)
	}
}
