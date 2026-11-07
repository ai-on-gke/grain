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

package systemlog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Unit names a systemd unit and which manager runs it: the system
// manager (PID 1), or the user manager of the account the daemon runs
// as (user@<uid>.service). The two keep separate journals as far as
// journalctl's selection goes -- a user unit is read with --user -- and
// grain is installed as one or the other depending on how it was set
// up: scripts/setup.sh writes the system unit grain-daemon.service,
// `grain init-workstation` the user unit grain.service.
type Unit struct {
	// Name is the unit's name, with its ".service" suffix.
	Name string
	// User is true for a unit of the user manager.
	User bool
}

// The two units grain installs itself as.
var (
	// DefaultDaemonUnit is scripts/setup.sh's system unit, the unit a
	// host deployment runs as, and what every reader here defaulted
	// to before the running unit was detected (OwnUnit).
	DefaultDaemonUnit = Unit{Name: "grain-daemon.service"}
	// WorkstationUnit is `grain init-workstation`'s user unit
	// (pkg/cli/init_workstation.go's renderWorkstationUnit).
	WorkstationUnit = Unit{Name: "grain.service", User: true}
)

// Scope is "user" or "system": the manager's name, as a response field
// and a sentence say it.
func (u Unit) Scope() string {
	if u.User {
		return "user"
	}
	return "system"
}

// String is the unit as an operator would say it: the name and, for a
// user unit, that it is one.
func (u Unit) String() string {
	if u.User {
		return u.Name + " (user unit)"
	}
	return u.Name
}

// Journal is the reader over this unit's journal.
func (u Unit) Journal() Journalctl { return Journalctl{Unit: u.Name, User: u.User} }

// LogCommand is the journalctl invocation an operator runs by hand to
// read this unit, for the sentences that tell them to.
func (u Unit) LogCommand() string {
	if u.User {
		return "journalctl --user -u " + u.Name
	}
	return "journalctl -u " + u.Name
}

// OwnUnit is the systemd unit this process runs under, read off its own
// control group (/proc/self/cgroup), and false when it runs under none
// it can see -- started from a shell, or inside a container whose cgroup
// namespace hides the host's hierarchy (which is how scripts/setup.sh's
// `docker run` under grain-daemon.service looks from within).
//
// The cgroup is the one answer to "which unit am I" that is right for a
// child process too: the `grain mcpserver` the daemon forks for a run
// inherits the daemon's cgroup, so the journal it reads for
// read_grain_log is the daemon's rather than one it guessed at.
func OwnUnit() (Unit, bool) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return Unit{}, false
	}
	return UnitOfCgroup(string(data))
}

// UnitOfCgroup is OwnUnit over the text of a /proc/<pid>/cgroup file.
//
// systemd puts every service in a cgroup named after it: a system unit
// under /system.slice/<unit> and a user unit under
// /user.slice/user-<uid>.slice/user@<uid>.service/<slice>/<unit>. The
// unit is the innermost ".service" component of the path, and it is a
// user unit when a user@<uid>.service component sits above it. A path
// whose innermost component is a scope (a shell's session-N.scope,
// docker's docker-<id>.scope) names no unit, and so does the bare "/" a
// container sees.
//
// Both cgroup v2's single "0::<path>" line and v1's per-controller
// "N:<controllers>:<path>" lines are read; on v1 the name=systemd
// hierarchy is the one systemd itself arranges, and the first line that
// names a unit wins.
func UnitOfCgroup(cgroup string) (Unit, bool) {
	for _, line := range strings.Split(cgroup, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if u, ok := unitOfCgroupPath(parts[2]); ok {
			return u, true
		}
	}
	return Unit{}, false
}

func unitOfCgroupPath(path string) (Unit, bool) {
	var unit Unit
	found := false
	for _, component := range strings.Split(path, "/") {
		switch {
		case strings.HasPrefix(component, "user@") && strings.HasSuffix(component, ".service"):
			// The user manager itself: what is beneath it is the user's.
			unit.User = true
			unit.Name, found = "", false
		case strings.HasSuffix(component, ".service"):
			unit.Name, found = component, true
		}
	}
	if !found {
		return Unit{}, false
	}
	return unit, true
}

// DaemonUnit is the unit whose journal is this daemon's own: the one the
// process runs under, or DefaultDaemonUnit when it cannot see one -- a
// container started by that unit's ExecStart, whose output docker
// forwards into that same unit's journal, is the case that fallback is
// for. Never a unit the process merely knows the name of: a user
// grain.service that happens to exist on a host whose daemon is the
// system unit is not read by it, and vice versa.
func DaemonUnit() Unit {
	if u, ok := OwnUnit(); ok {
		return u
	}
	return DefaultDaemonUnit
}

// DaemonJournal is the "daemon" log source on a host deployment: the
// running unit's journal, with the other grain unit this host also has
// installed reachable by name. It exists because a host can carry both
// -- a workstation that ran scripts/setup.sh once and `grain
// init-workstation` later -- and reading the wrong one of the two
// answers a question about a process that is not serving the request
// with a journal that looks perfectly plausible.
//
// Tail and Read are the running unit's. Units lists what a caller may
// choose between, the running unit first, and Journal selects one of
// them by name; nothing here reads a unit that is not in that list.
type DaemonJournal struct {
	// Running is the unit this process runs under (DaemonUnit).
	Running Unit
	// Others is every other grain unit installed on this host, in the
	// order they are offered.
	Others []Unit
}

// DetectDaemonJournal is the DaemonJournal for a host whose running unit
// is running and whose installed units installed reports: each of the
// two units grain installs that is not the running one and is installed
// goes in Others. installed nil means UnitInstalled.
func DetectDaemonJournal(running Unit, installed func(Unit) bool) DaemonJournal {
	if installed == nil {
		installed = UnitInstalled
	}
	d := DaemonJournal{Running: running}
	for _, u := range []Unit{DefaultDaemonUnit, WorkstationUnit} {
		if u == running || !installed(u) {
			continue
		}
		d.Others = append(d.Others, u)
	}
	return d
}

// UnitInstalled reports whether u's unit file is on this host: under
// /etc/systemd/system for a system unit, and under the user manager's
// configuration directory ($XDG_CONFIG_HOME/systemd/user, or
// ~/.config/systemd/user -- userUnitDir in pkg/cli) for a user unit. A
// unit file is a cheap, side-effect-free sign that a unit exists;
// asking systemctl would be another subprocess per request for the
// same answer.
func UnitInstalled(u Unit) bool {
	for _, dir := range unitDirs(u) {
		if _, err := os.Stat(filepath.Join(dir, u.Name)); err == nil {
			return true
		}
	}
	return false
}

func unitDirs(u Unit) []string {
	if !u.User {
		return []string{"/etc/systemd/system"}
	}
	var dirs []string
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		dirs = append(dirs, filepath.Join(x, "systemd", "user"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config", "systemd", "user"))
	}
	return dirs
}

// Tail is the running unit's Journalctl.Tail.
func (d DaemonJournal) Tail(ctx context.Context, lines int) ([]string, error) {
	return d.Running.Journal().Tail(ctx, lines)
}

// Read is the running unit's Journalctl.Read.
func (d DaemonJournal) Read(ctx context.Context, q Query) ([]string, error) {
	return d.Running.Journal().Read(ctx, q)
}

// Units is every unit this source can read, the running one first.
func (d DaemonJournal) Units() []Unit {
	return append([]Unit{d.Running}, d.Others...)
}

// Journal is the reader for the unit named name, which must be one of
// Units: the running unit, or another grain unit installed here. Any
// other name is refused with the list, rather than passed to journalctl
// -- which would happily read any unit on the host.
func (d DaemonJournal) Journal(name string) (Journalctl, error) {
	for _, u := range d.Units() {
		if u.Name == name {
			return u.Journal(), nil
		}
	}
	var names []string
	for _, u := range d.Units() {
		names = append(names, u.Name)
	}
	return Journalctl{}, fmt.Errorf("no grain unit %q on this host: the units here are %s", name, strings.Join(names, ", "))
}
