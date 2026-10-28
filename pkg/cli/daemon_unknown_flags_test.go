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

// daemon_unknown_flags_test.go covers the rule this deployment learned
// the hard way (grain/task-233): a `grain daemon` handed a flag it does
// not define starts anyway, saying so once, rather than exiting into a
// restart loop.
//
// The command lines here are the real shape -- scripts/setup.sh's own
// daemon_args, with the flag the image had dropped sitting in the middle
// of them -- because the properties worth holding are all about the
// flags *around* the unknown one: they have to be parsed exactly as they
// would have been, once each, with nothing swallowed and nothing
// repeated.
package cli

import (
	"flag"
	"strings"
	"testing"
)

// unitFlagSet is a stand-in for the daemon's own flag set with one of
// each kind of flag in it: a string that takes a value, a bool that does
// not, and the repeatable Var (-granule-create-arg) whose Set is called
// once per occurrence.
func unitFlagSet() (*flag.FlagSet, *stringSliceFlag) {
	fs := flag.NewFlagSet("grain daemon", flag.ContinueOnError)
	fs.String("data-dir", "", "")
	fs.String("sandbox-dir", "", "")
	fs.Int("max-workers", 1, "")
	fs.Bool("granule-sandboxes", false, "")
	var createArgs stringSliceFlag
	fs.Var(&createArgs, "granule-create-arg", "")
	return fs, &createArgs
}

func TestADaemonStartsPastAFlagThisBuildNoLongerDefines(t *testing.T) {
	t.Parallel()
	fs, _ := unitFlagSet()
	// 2026-09-18, verbatim in shape: a unit written before
	// grain/task-193 removed the root shell, running an image from after
	// it.
	unknown, err := readDaemonFlags(fs, []string{
		"-data-dir", "/var/lib/grain/data",
		"-root-shell-control-dir", "/var/lib/grain/data/control",
		"-max-workers", "3",
	})
	if err != nil {
		t.Fatalf("readDaemonFlags = %v, want a daemon that comes up", err)
	}
	if len(unknown) != 1 || unknown[0] != "-root-shell-control-dir" {
		t.Fatalf("unknown = %v, want exactly -root-shell-control-dir", unknown)
	}
	// The flag after the unknown one is the one an over-eager drop would
	// have eaten, and the deployment would have come up with one worker
	// instead of three without a word said.
	if got := fs.Lookup("max-workers").Value.String(); got != "3" {
		t.Errorf("-max-workers = %s, want 3 -- the flags around an ignored one are parsed unchanged", got)
	}
	if got := fs.Lookup("data-dir").Value.String(); got != "/var/lib/grain/data" {
		t.Errorf("-data-dir = %s, want /var/lib/grain/data", got)
	}
}

// The value of an unknown flag goes with it, and is not left behind to
// be read as this build's first positional argument.
func TestAnIgnoredFlagTakesItsOwnValueWithIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"separate value", []string{"-root-shell-control-dir", "/var/lib/grain/data/control", "-max-workers", "2"}},
		{"attached value", []string{"-root-shell-control-dir=/var/lib/grain/data/control", "-max-workers", "2"}},
		{"double dash", []string{"--root-shell-control-dir", "/var/lib/grain/data/control", "-max-workers", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, _ := unitFlagSet()
			unknown, err := readDaemonFlags(fs, tc.args)
			if err != nil {
				t.Fatalf("readDaemonFlags = %v", err)
			}
			if len(unknown) != 1 || unknown[0] != "-root-shell-control-dir" {
				t.Fatalf("unknown = %v", unknown)
			}
			if got := fs.Lookup("max-workers").Value.String(); got != "2" {
				t.Errorf("-max-workers = %s, want 2", got)
			}
			if rest := fs.Args(); len(rest) != 0 {
				t.Errorf("leftover arguments %v -- an ignored flag's value was left on the line", rest)
			}
		})
	}
}

// A boolean this build dropped is followed by the next flag, not by a
// value, and dropping that flag with it would turn one ignored setting
// into two.
func TestAnIgnoredBooleanDoesNotSwallowTheFlagAfterIt(t *testing.T) {
	t.Parallel()
	fs, _ := unitFlagSet()
	unknown, err := readDaemonFlags(fs, []string{"-root-shell", "-granule-sandboxes", "-max-workers", "4"})
	if err != nil {
		t.Fatalf("readDaemonFlags = %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "-root-shell" {
		t.Fatalf("unknown = %v, want only -root-shell", unknown)
	}
	if fs.Lookup("granule-sandboxes").Value.String() != "true" {
		t.Error("-granule-sandboxes was eaten as the ignored flag's value; this deployment would have " +
			"dispatched onto the host instead of into VMs")
	}
}

// A repeatable flag's values are set once each. This is why the unknown
// flags are removed before the parse rather than by re-parsing what is
// left after each failure -- see readDaemonFlags' own comment.
func TestIgnoringAFlagDoesNotRepeatTheOnesBeforeIt(t *testing.T) {
	t.Parallel()
	fs, createArgs := unitFlagSet()
	if _, err := readDaemonFlags(fs, []string{
		"-granule-create-arg", "-disk-mode=overlay",
		"-granule-create-arg", "-guest-port", "-granule-create-arg", "22",
		"-root-shell-control-dir", "/var/lib/grain/data/control",
	}); err != nil {
		t.Fatalf("readDaemonFlags = %v", err)
	}
	want := []string{"-disk-mode=overlay", "-guest-port", "22"}
	if got := []string(*createArgs); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("-granule-create-arg = %v, want %v -- every VM would have been created with each argument twice", got, want)
	}
}

// A value a flag *this build defines* cannot take is still fatal: it is
// this build's own setting, said wrongly, and there is nothing to fall
// back on. What changed is only how much gets printed on the way out.
func TestABadValueForADefinedFlagIsStillAnError(t *testing.T) {
	t.Parallel()
	fs, _ := unitFlagSet()
	unknown, err := readDaemonFlags(fs, []string{"-max-workers", "banana"})
	if err == nil {
		t.Fatal("readDaemonFlags = nil, want a daemon that refuses a worker count that is not a number")
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want none", unknown)
	}
	if !strings.Contains(err.Error(), "max-workers") {
		t.Errorf("error = %q, want it to name the flag", err)
	}
}

// -h still reaches the flag package as -h, so a human at a terminal gets
// the whole flag list (daemonUsage) rather than being told their -h is
// an unknown flag.
func TestHelpIsNotAnUnknownFlag(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"-h", "-help", "--help"} {
		fs, _ := unitFlagSet()
		unknown, err := readDaemonFlags(fs, []string{arg})
		if err != flag.ErrHelp {
			t.Errorf("readDaemonFlags(%s) = %v, want flag.ErrHelp", arg, err)
		}
		if len(unknown) != 0 {
			t.Errorf("readDaemonFlags(%s) reported %v as undefined", arg, unknown)
		}
	}
}

// The journal cost of the report is fixed: two lines per start, however
// far apart the unit and the image have drifted. The dump this replaced
// was ~200 lines, and 52 restarts of it buried every other log line on
// the deployment for the eight minutes it was down.
func TestTheUnknownFlagReportIsBoundedWhateverTheDisagreement(t *testing.T) {
	t.Parallel()
	if got := unknownFlagReport(nil); got != nil {
		t.Fatalf("report with nothing to report = %v, want nothing printed at all", got)
	}
	var many []string
	for i := 0; i < maxReportedUnknownFlags*5; i++ {
		many = append(many, "-gone-"+strings.Repeat("x", i))
	}
	report := unknownFlagReport(many)
	if len(report) != 2 {
		t.Fatalf("report = %d lines, want 2", len(report))
	}
	if !strings.Contains(report[0], "ignoring 60 flag(s)") {
		t.Errorf("report[0] = %q, want the exact count even when the list is cut short", report[0])
	}
	if !strings.Contains(report[0], "(and 48 more)") {
		t.Errorf("report[0] = %q, want the list capped at %d names", report[0], maxReportedUnknownFlags)
	}
	// And it says what to do, which is the half a flag name alone does
	// not give: the unit is on the host, not in the image.
	if !strings.Contains(report[1], "setup.sh") {
		t.Errorf("report[1] = %q, want the fix named", report[1])
	}
}
