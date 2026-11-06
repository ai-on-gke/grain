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

package systemlog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/systemlog"
)

// The unit this process runs under is read off its cgroup: the system
// unit under system.slice, the user unit under user@<uid>.service, and
// none at all for a shell's scope, a docker scope or a container's bare
// root -- the case that must not be mistaken for the system unit, since
// the fallback for it is a separate decision (DaemonUnit).
func TestUnitOfCgroupTellsSystemFromUserUnits(t *testing.T) {
	for _, c := range []struct {
		name   string
		cgroup string
		want   systemlog.Unit
		ok     bool
	}{
		{"system unit, cgroup v2", "0::/system.slice/grain-daemon.service\n", systemlog.DefaultDaemonUnit, true},
		{"user unit, cgroup v2",
			"0::/user.slice/user-1000.slice/user@1000.service/app.slice/grain.service\n",
			systemlog.WorkstationUnit, true},
		{"system unit, cgroup v1",
			"12:pids:/system.slice/grain-daemon.service\n1:name=systemd:/system.slice/grain-daemon.service\n",
			systemlog.DefaultDaemonUnit, true},
		{"user manager itself", "0::/user.slice/user-1000.slice/user@1000.service/init.scope\n", systemlog.Unit{}, false},
		{"a shell session", "0::/user.slice/user-1000.slice/session-3.scope\n", systemlog.Unit{}, false},
		{"docker scope", "0::/system.slice/docker-0123456789abcdef.scope\n", systemlog.Unit{}, false},
		{"container root", "0::/\n", systemlog.Unit{}, false},
		{"empty", "", systemlog.Unit{}, false},
	} {
		got, ok := systemlog.UnitOfCgroup(c.cgroup)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: UnitOfCgroup(%q) = %+v, %v; want %+v, %v", c.name, c.cgroup, got, ok, c.want, c.ok)
		}
	}
}

// A user unit's reader asks journalctl for the user journal, and says
// so in the command its errors and operator-facing sentences name; the
// system unit's does neither.
func TestUnitJournalSelectsTheManager(t *testing.T) {
	if got := systemlog.WorkstationUnit.Journal(); got != (systemlog.Journalctl{Unit: "grain.service", User: true}) {
		t.Errorf("WorkstationUnit.Journal() = %+v", got)
	}
	if got := systemlog.DefaultDaemonUnit.Journal(); got != (systemlog.Journalctl{Unit: "grain-daemon.service"}) {
		t.Errorf("DefaultDaemonUnit.Journal() = %+v", got)
	}
	if got := systemlog.WorkstationUnit.LogCommand(); got != "journalctl --user -u grain.service" {
		t.Errorf("WorkstationUnit.LogCommand() = %q", got)
	}
	if got := systemlog.DefaultDaemonUnit.LogCommand(); got != "journalctl -u grain-daemon.service" {
		t.Errorf("DefaultDaemonUnit.LogCommand() = %q", got)
	}
	if got, want := systemlog.WorkstationUnit.Scope(), "user"; got != want {
		t.Errorf("WorkstationUnit.Scope() = %q, want %q", got, want)
	}
	if got, want := systemlog.DefaultDaemonUnit.Scope(), "system"; got != want {
		t.Errorf("DefaultDaemonUnit.Scope() = %q, want %q", got, want)
	}
}

// The daemon source reads the running unit by default and offers the
// other grain unit only when it is installed here; a unit by any other
// name is refused rather than handed to journalctl.
func TestDaemonJournalOffersTheOtherInstalledUnitByName(t *testing.T) {
	installed := func(set ...systemlog.Unit) func(systemlog.Unit) bool {
		return func(u systemlog.Unit) bool {
			for _, s := range set {
				if s == u {
					return true
				}
			}
			return false
		}
	}

	// A workstation daemon on a host that also has the system unit.
	both := systemlog.DetectDaemonJournal(systemlog.WorkstationUnit, installed(systemlog.WorkstationUnit, systemlog.DefaultDaemonUnit))
	if got := both.Units(); len(got) != 2 || got[0] != systemlog.WorkstationUnit || got[1] != systemlog.DefaultDaemonUnit {
		t.Fatalf("Units() with both installed = %+v, want the running user unit first, then the system unit", got)
	}
	j, err := both.Journal("grain-daemon.service")
	if err != nil || j != (systemlog.Journalctl{Unit: "grain-daemon.service"}) {
		t.Errorf("Journal(grain-daemon.service) = %+v, %v; want the system unit's reader", j, err)
	}
	j, err = both.Journal("grain.service")
	if err != nil || j != (systemlog.Journalctl{Unit: "grain.service", User: true}) {
		t.Errorf("Journal(grain.service) = %+v, %v; want the user unit's reader", j, err)
	}
	if _, err := both.Journal("sshd.service"); err == nil || !strings.Contains(err.Error(), "grain.service, grain-daemon.service") {
		t.Errorf("Journal(sshd.service) = %v, want a refusal listing the grain units", err)
	}

	// The ordinary host deployment: the system unit alone.
	alone := systemlog.DetectDaemonJournal(systemlog.DefaultDaemonUnit, installed(systemlog.DefaultDaemonUnit))
	if got := alone.Units(); len(got) != 1 || got[0] != systemlog.DefaultDaemonUnit {
		t.Errorf("Units() with the system unit alone = %+v, want just it", got)
	}
	if _, err := alone.Journal("grain.service"); err == nil {
		t.Error("Journal(grain.service) with no user unit installed succeeded; want a refusal")
	}

	// The running unit is offered even when its unit file is not where
	// UnitInstalled looks: it is running, which is the better evidence.
	running := systemlog.DetectDaemonJournal(systemlog.WorkstationUnit, installed())
	if got := running.Units(); len(got) != 1 || got[0] != systemlog.WorkstationUnit {
		t.Errorf("Units() with nothing installed = %+v, want the running unit alone", got)
	}
}

// UnitInstalled looks where each manager keeps its unit files: the user
// unit under $XDG_CONFIG_HOME/systemd/user.
func TestUnitInstalledReadsTheUserUnitDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if systemlog.UnitInstalled(systemlog.WorkstationUnit) {
		t.Fatal("UnitInstalled(grain.service) with no unit file = true")
	}
	unitDir := filepath.Join(dir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "grain.service"), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !systemlog.UnitInstalled(systemlog.WorkstationUnit) {
		t.Error("UnitInstalled(grain.service) with the unit file written = false")
	}
}
